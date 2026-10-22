#!/usr/bin/env bash
# Runs DeedFlow in a GitHub Codespace (or any Debian/Ubuntu machine with sudo):
#
#   bash scripts/codespace.sh
#
# It installs Postgres (and the OCR tools) if they are missing, builds the
# binary, starts the web server and worker on port 8001, and makes sure there
# is a login "admin" with password "admin" (a site administrator and the admin
# of an organization). In a Codespace open port 8001 from the Ports tab or the
# address printed at the end.
#
# The app runs directly in the Codespace, not in Docker Compose: container to
# container networking is unreliable in Codespaces (the web container timed
# out reaching the database container).
set -euo pipefail

cd "$(dirname "$0")/.."

PORT=8001
ADMIN_USER=admin
ADMIN_PASSWORD=admin
ORGANIZATION="DeedFlow Office"
LOG=deedflow.log
PIDFILE=deedflow.pid

random() { openssl rand -hex 24; }

# --- .env -------------------------------------------------------------------
if [ ! -f .env ]; then
  echo "Writing .env from .env.example"
  cp .env.example .env
  # A Fernet key is 32 random bytes in URL-safe base64.
  fernet_key=$(openssl rand -base64 32 | tr '+/' '-_')
  sed -i \
    -e "s|^SECRET_KEY=.*|SECRET_KEY=$(random)|" \
    -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$(random)|" \
    -e "s|^FIELD_ENCRYPTION_KEY=.*|FIELD_ENCRYPTION_KEY=${fernet_key}|" \
    .env
fi

# The forwarded address of a Codespace is https://<name>-8001.app.github.dev.
if ! grep -q '^ALLOWED_HOSTS=.*\.app\.github\.dev' .env; then
  sed -i -e 's|^ALLOWED_HOSTS=\(.*\)$|ALLOWED_HOSTS=\1,.app.github.dev|' .env
fi
if [ -n "${CODESPACE_NAME:-}" ]; then
  domain="${GITHUB_CODESPACES_PORT_FORWARDING_DOMAIN:-app.github.dev}"
  site_url="https://${CODESPACE_NAME}-${PORT}.${domain}"
  if grep -q '^#\? *SITE_URL=' .env; then
    sed -i -e "s|^#\? *SITE_URL=.*|SITE_URL=${site_url}|" .env
  else
    echo "SITE_URL=${site_url}" >> .env
  fi
else
  site_url="http://localhost:${PORT}"
fi

set -a
# shellcheck disable=SC1091
. ./.env
set +a
# Here the database is the local Postgres, not the compose "db" service.
export POSTGRES_HOST=127.0.0.1 POSTGRES_PORT=5432 HTTP_ADDR=":${PORT}" MEDIA_ROOT="$PWD/media"
export TESSERACT_CMD="$(command -v tesseract || echo /usr/bin/tesseract)"

# --- Postgres and tools -----------------------------------------------------
missing=()
command -v pg_ctlcluster >/dev/null 2>&1 || missing+=(postgresql)
command -v tesseract >/dev/null 2>&1 || missing+=(tesseract-ocr)
command -v pdftoppm >/dev/null 2>&1 || missing+=(poppler-utils)
if [ ${#missing[@]} -gt 0 ]; then
  echo "Installing ${missing[*]}"
  sudo apt-get update -qq
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${missing[@]}" >/dev/null
  export TESSERACT_CMD="$(command -v tesseract || echo /usr/bin/tesseract)"
fi

echo "Starting Postgres"
sudo service postgresql start >/dev/null
for _ in $(seq 1 30); do
  sudo -u postgres psql -qtAc 'SELECT 1' >/dev/null 2>&1 && break
  sleep 1
done
sudo -u postgres psql -q -v ON_ERROR_STOP=1 \
  -v user="$POSTGRES_USER" -v pass="$POSTGRES_PASSWORD" <<'SQL'
SELECT format('CREATE ROLE %I LOGIN', :'user')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'user') \gexec
ALTER ROLE :"user" WITH LOGIN PASSWORD :'pass';
SQL
if ! sudo -u postgres psql -qtAc "SELECT 1 FROM pg_database WHERE datname = '${POSTGRES_DB}'" | grep -q 1; then
  sudo -u postgres createdb -O "$POSTGRES_USER" "$POSTGRES_DB"
fi

# --- build and start --------------------------------------------------------
echo "Building DeedFlow"
GOTOOLCHAIN=auto go build -o bin/deedflow ./cmd/deedflow

if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
  echo "Stopping the previous run"
  kill "$(cat "$PIDFILE")" || true
  sleep 2
fi
# An earlier Docker Compose run of this script may still hold the port.
if command -v docker >/dev/null 2>&1 && docker compose ps -q 2>/dev/null | grep -q .; then
  echo "Stopping the Docker Compose containers from an earlier run"
  docker compose down >/dev/null 2>&1 || true
fi

echo "Starting the web server and worker (log: $LOG)"
nohup bin/deedflow all >"$LOG" 2>&1 &
echo $! >"$PIDFILE"

echo -n "Waiting for the website"
for _ in $(seq 1 60); do
  if curl -fsS -o /dev/null "http://localhost:${PORT}/login/" 2>/dev/null; then
    echo " - up"
    break
  fi
  if ! kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
    break
  fi
  echo -n "."
  sleep 1
done
if ! curl -fsS -o /dev/null "http://localhost:${PORT}/login/" 2>/dev/null; then
  echo
  echo "The website didn't start. The end of $LOG:" >&2
  tail -n 40 "$LOG" >&2
  exit 1
fi

# --- admin / admin -----------------------------------------------------------
# A site administrator (for /admin/) ...
bin/deedflow createsuperuser \
  --username "$ADMIN_USER" --email "admin@example.com" --password "$ADMIN_PASSWORD" >/dev/null 2>&1 || true
# ... who is also the admin of an organization (for the deeds and accounts
# pages). Run again, this keeps the organization and resets the password.
printf '%s\n' "$ADMIN_PASSWORD" | bin/deedflow setup_organization \
  "$ORGANIZATION" --admin "$ADMIN_USER" --set-password >/dev/null

cat <<EOF

DeedFlow is running.

  Website:  ${site_url}/login/
  Login:    ${ADMIN_USER} / ${ADMIN_PASSWORD}
  Admin:    ${site_url}/admin/

Log:   tail -f $LOG
Stop:  kill \$(cat $PIDFILE)
EOF
