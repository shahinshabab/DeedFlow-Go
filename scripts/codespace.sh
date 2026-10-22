#!/usr/bin/env bash
# Runs DeedFlow in a GitHub Codespace (or any machine with Docker and Go):
#
#   bash scripts/codespace.sh
#
# It starts Postgres in Docker, installs the OCR tools when it can, builds the
# binary, starts the web server and worker on port 8001, and makes sure there
# is a login "admin" with password "admin" (a site administrator and the admin
# of an organization). In a Codespace open port 8001 from the Ports tab or the
# address printed at the end.
#
# The app runs directly in the Codespace, not in Docker Compose: container to
# container networking is unreliable in Codespaces (the web container timed
# out reaching the database container). Nothing here needs a sudo password.
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
# Here the database is the Postgres container on the host network, not the
# compose "db" service.
unset DATABASE_URL
export POSTGRES_HOST=127.0.0.1 POSTGRES_PORT=5434 HTTP_ADDR=":${PORT}" MEDIA_ROOT="$PWD/media"
export TESSERACT_CMD="$(command -v tesseract || echo /usr/bin/tesseract)"

# --- Postgres and tools -----------------------------------------------------
# Postgres runs in Docker on the host network (no sudo needed, and no
# container-to-container networking, which is what failed in Codespaces).
# It listens on 5434 so it never meets another Postgres already on 5432.
DB_PORT=5434
DB_CONTAINER=deedflow-codespace-pg
if ! command -v docker >/dev/null 2>&1; then
  echo "Docker isn't installed here; the default Codespace image has it." >&2
  exit 1
fi
# The container from an earlier version of this script listened on 5432.
docker rm -f deedflow-codespace-db >/dev/null 2>&1 || true
if [ "$(docker container inspect -f '{{.State.Running}}' "$DB_CONTAINER" 2>/dev/null)" != "true" ]; then
  docker rm -f "$DB_CONTAINER" >/dev/null 2>&1 || true
  echo "Starting Postgres"
  docker run -d --name "$DB_CONTAINER" --network host --restart unless-stopped \
    -e POSTGRES_DB="$POSTGRES_DB" -e POSTGRES_USER="$POSTGRES_USER" \
    -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
    -v deedflow_codespace_pg:/var/lib/postgresql/data \
    postgres:16-alpine -p "$DB_PORT" >/dev/null
fi
echo -n "Waiting for Postgres"
db_up=
for _ in $(seq 1 60); do
  if docker exec "$DB_CONTAINER" pg_isready -q -h 127.0.0.1 -p "$DB_PORT" -U "$POSTGRES_USER" -d "$POSTGRES_DB" 2>/dev/null; then
    db_up=1
    echo " - up"
    break
  fi
  echo -n "."
  sleep 1
done
if [ -z "$db_up" ]; then
  echo
  echo "Postgres didn't start. Its log:" >&2
  docker logs --tail 40 "$DB_CONTAINER" >&2 || true
  exit 1
fi
# The data volume keeps the password it was created with; set it to the one in
# .env (over the container's socket, which needs no password).
docker exec -i "$DB_CONTAINER" psql -q -v ON_ERROR_STOP=1 -p "$DB_PORT" \
  -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null <<SQL
ALTER USER "$POSTGRES_USER" WITH PASSWORD '$POSTGRES_PASSWORD';
SQL

# OCR tools are only needed to process uploaded PDFs and solve the portal
# captcha; install them when sudo works without a password.
missing=()
command -v tesseract >/dev/null 2>&1 || missing+=(tesseract-ocr)
command -v pdftoppm >/dev/null 2>&1 || missing+=(poppler-utils)
if [ ${#missing[@]} -gt 0 ]; then
  if sudo -n true 2>/dev/null; then
    echo "Installing ${missing[*]}"
    sudo apt-get update -qq
    sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${missing[@]}" >/dev/null
    export TESSERACT_CMD="$(command -v tesseract || echo /usr/bin/tesseract)"
  else
    echo "Note: ${missing[*]} not installed (no sudo); the site works, PDF processing won't."
  fi
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
