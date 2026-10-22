#!/usr/bin/env bash
# Runs DeedFlow in a GitHub Codespace (or any machine with Docker):
#
#   bash scripts/codespace.sh
#
# It writes a .env on the first run, starts the app with Docker Compose, and
# makes sure there is a login "admin" with password "admin" (a site
# administrator and the admin of an organization). The site is on port 8001;
# in a Codespace open it from the Ports tab or the address printed at the end.
set -euo pipefail

cd "$(dirname "$0")/.."

PORT=8001
ADMIN_USER=admin
ADMIN_PASSWORD=admin
ORGANIZATION="DeedFlow Office"

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker isn't installed here. Use a Codespace with Docker (the default image has it)." >&2
  exit 1
fi

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

# --- start --------------------------------------------------------------------
echo "Building and starting DeedFlow (the first run takes a few minutes)"
docker compose up -d --build db web worker

echo -n "Waiting for the website"
for _ in $(seq 1 120); do
  if curl -fsS -o /dev/null "http://localhost:${PORT}/login/"; then
    echo " - up"
    break
  fi
  echo -n "."
  sleep 2
done
if ! curl -fsS -o /dev/null "http://localhost:${PORT}/login/"; then
  echo
  echo "The website didn't start. Its log:" >&2
  docker compose logs --tail 50 web >&2
  exit 1
fi

# --- admin / admin -----------------------------------------------------------
# A site administrator (for /admin/) ...
docker compose exec -T web deedflow createsuperuser \
  --username "$ADMIN_USER" --email "admin@example.com" --password "$ADMIN_PASSWORD" >/dev/null 2>&1 || true
# ... who is also the admin of an organization (for the deeds and accounts
# pages). Run again, this keeps the organization and resets the password.
printf '%s\n' "$ADMIN_PASSWORD" | docker compose exec -T web deedflow setup_organization \
  "$ORGANIZATION" --admin "$ADMIN_USER" --set-password >/dev/null

cat <<EOF

DeedFlow is running.

  Website:  ${site_url}/login/
  Login:    ${ADMIN_USER} / ${ADMIN_PASSWORD}
  Admin:    ${site_url}/admin/

Logs:  docker compose logs -f web worker
Stop:  docker compose down
EOF
