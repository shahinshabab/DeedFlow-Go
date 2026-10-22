# DeedFlow (Go)

DeedFlow automates preparing and filing property deed registrations with the Kerala
Registration Department's online portal. The workflow: upload a scanned/typed Malayalam
deed PDF, extract its text, use an AI model to structure it into the portal's data model,
review/correct it through a multi-step form, then have a background worker log into the
department portal (solving the captcha itself) and fill in the submission on the user's
behalf, with live progress shown back in the browser over a WebSocket. It also keeps the
office's Accounts (payment receipts collected from the portal, own payments with bills),
reads PageMaker / InDesign / PDF files (File reader, Filing sheets, conversion to InDesign).

This repository is a rewrite of the Python/Django project
([DeedFlow](https://github.com/shahinshabab/DeedFlow)) in Go. It keeps the same database
schema, URLs, pages, templates and behaviour, so it runs against an existing DeedFlow
database; the differences are in how it is put together:

| Django project | This project |
|---|---|
| Django + Daphne (ASGI) | one static Go binary (`net/http`), templates and static files embedded |
| Celery + Redis + beat | a job queue in Postgres (`SELECT ... FOR UPDATE SKIP LOCKED`), advisory locks, a scheduler table for the nightly receipt collection |
| Channels + Redis layer | WebSocket hub fed by Postgres `LISTEN/NOTIFY` |
| `pmd2json` (C++ libpagemaker) for PageMaker files | a Go port of libpagemaker (`internal/pmd`), byte-for-byte equal to `pmd2json` on the files it was tested with |
| pdfplumber / pdfminer | a Go port of the parts DeedFlow uses (`internal/plumber`) |
| selenium (Python) | a W3C WebDriver client (`internal/webdriver`) talking to the same Selenium container |
| `django.contrib.admin` (`/admin/`) | a re-implementation driven by a description of the Django admin (`internal/web/admin_spec.json`): same pages, Django's own admin templates and static files, changelists with search / filters / sorting / actions, add and change forms with inlines, history, delete confirmation, user and group management, popups and autocomplete |

## Getting started

Everything runs in Docker; you only need **Docker** and **Git**.

```bash
git clone https://github.com/shahinshabab/DeedFlow-Go.git
cd DeedFlow-Go
cp .env.example .env
```

Fill in `.env` (at least `SECRET_KEY`, `POSTGRES_PASSWORD` and `FIELD_ENCRYPTION_KEY`; the AI keys
are needed for AI structuring). Generate the encryption key with the binary itself:

```bash
docker compose run --rm --no-deps web genkey
```

Then:

```bash
docker compose up --build            # add -d to run in the background
docker compose exec web deedflow createsuperuser
```

and open **http://localhost:8001/login/**. The `web` container creates and updates the
database tables when it starts.

| What | Address |
|---|---|
| The app | http://localhost:8001 |
| Adminer (Postgres browser) | http://localhost:8083 (System: PostgreSQL, Server: `db`) |
| Selenium noVNC (watch the browser during a submission) | http://localhost:7900, password `secret` |

### In a GitHub Codespace

```bash
bash scripts/codespace.sh
```

writes a `.env` (random secrets, the Codespace's forwarded address allowed), starts the database,
web server and worker with Docker Compose, and makes sure the login **admin** / **admin** exists
(site administrator and admin of an organization). Open port 8001 from the Ports tab, or the address
the script prints. Run it again at any time; it keeps the data and resets admin's password to `admin`.

### Without Docker

You need Go 1.25+, a Postgres database, Tesseract (`tesseract`) and poppler (`pdftoppm`); a
Selenium server (standalone Chrome) is only needed for portal submission.

```bash
go build -o deedflow ./cmd/deedflow
POSTGRES_HOST=127.0.0.1 POSTGRES_PASSWORD=... FIELD_ENCRYPTION_KEY=$(./deedflow genkey) ./deedflow all
```

`all` runs the web server and a worker in one process; `serve` and `worker` run them separately.

## Commands

`deedflow <command>` replaces `manage.py <command>`:

| Command | |
|---|---|
| `serve`, `worker`, `all` | web server, background worker, both |
| `migrate` | create / update the database tables |
| `createsuperuser`, `setup_organization`, `genkey` | accounts and keys |
| `seed_test_deeds`, `run_test_submissions`, `run_test_edit` | test deeds and submissions against a stand-in or the real portal |
| `scrape_portal_dropdowns`, `probe_portal_page`, `probe_row_actions`, `probe_fair_value` | read-only portal reconnaissance |
| `send_test_email` | check the mail settings |

## Configuration

The variable names are the Django project's, so an existing `.env` keeps working
(`.env.example` lists them): `SECRET_KEY`, `DEBUG`, `ALLOWED_HOSTS`, `FIELD_ENCRYPTION_KEY`,
`POSTGRES_*` (or `DATABASE_URL`), `SELENIUM_REMOTE_URL`, `OPENAI_*`, `ANTHROPIC_*`,
`AI_CLAUDE_FALLBACK`, `TESSERACT_CMD`, SES / SMTP settings, `SITE_URL`, `HTTP_ADDR`,
`MEDIA_ROOT`. Redis and Celery settings are no longer used.

## Project layout

```
cmd/deedflow/          the binary
internal/
  cli/                 serve / worker / management commands
  web/                 HTTP handlers, routing, template engine glue, sessions, CSRF
  store/, models/, db/ Postgres access (hand-written SQL + a small reflection mapper)
  migrations/          SQL migrations (0001 is the Django schema as of the port)
  jobs/, events/       Postgres job queue; LISTEN/NOTIFY hub for the live status socket
  extract/, plumber/   PDF text/table extraction (pdfminer/pdfplumber port), textconv/ legacy-font conversion
  ai/, deeds/, pipeline/   AI structuring, deed rules + payload, the processing and submission jobs
  deedforms/, forms/   Django-faithful model forms and formsets for the wizard
  portal/, webdriver/  portal automation (login, captcha, token rows, scraping) on a WebDriver client
  ledger/, mail/       receipt collection, alerts, Excel, SES / SMTP
  filereader/, pmd/    File reader, filing sheets, IDML / JSX writers; PageMaker parser
    admin_*.go         the admin site (changelist, forms, inline formsets, widgets, deletion collector, user admin)
web/templates, web/static   the pages and their assets (web/templates/admin and web/static/admin are Django's own admin files)
tools/                 comparison and oracle scripts (see Testing)
```

## Testing

```bash
go test ./...
```

Tests that need a database use `TEST_DATABASE_URL` (the compose database by default) and skip when
it can't be reached. The page tests (`internal/web/*_test.go`) are the Go counterparts of the Django
project's `tests.py` files: they drive the real application over HTTP (real login form, CSRF,
uploads) and remove what they create; the few that replace shared tables (the scraped portal
options, queued jobs) run on a scratch database that is created and dropped for the test, so the
database user needs `CREATEDB`. The rest of the Django tests live next to the code they test:
accounts and `setup_organization` (`internal/web`, `internal/cli`), payload rules, placeholders and
preflight (`internal/deeds`), table extraction (`internal/extract`), submission and row-edit
scenarios against a scripted portal (`internal/pipeline`), receipt collection (`internal/ledger`),
and the AI provider choice (`internal/ai`). Each test that uses the database brings its tables up to
date first, so an empty database works. The comparison of `internal/pmd` with the C++ `pmd2json`
needs `pmd2json` on `PATH` and skips without it; the browser tests start `chromedriver` (`CHROMEDRIVER`, `CHROME_BIN`)
against a mock portal and skip when no matching browser exists.

Because the aim is behaviour identical to the Django site, much of the verification compares the two:

- `tools/compare/pages.py` renders the same pages from a running Django site and this one (same
  database) and diffs the normalised HTML; `flow.py`, `ledger_flow.py` and `filereader_flow.py`
  drive whole scenarios (forms, uploads, downloads, JSON endpoints) against both and compare
  the answers and the rows left behind.
- `tools/compare/admin_pages.py` compares admin pages (changelists with many query strings, add / change /
  history / delete pages) with the Django admin; `tools/compare/admin_flow.py` runs the admin scenarios (adding,
  changing, deleting through the forms, inlines, uploads, password changes, popups, a limited staff account,
  validation fuzzing) against both and compares the answers and the rows and log entries left behind.
  `tools/admin/gen_spec.py` writes the description of the Django admin the Go admin is driven by
  (`internal/web/admin_spec.json`; run it again when the models or model admins change) and
  `tools/admin/gen_oracle.py` the expected results `internal/web/admin_test.go` checks the re-implemented
  parsing and validation against.
- `tools/filereader/oracle.py` runs the Django file reader / filing sheet / IDML / JSX code on
  generated and real files and dumps the results for `internal/filereader` to be compared with;
  `tools/pmdfuzz` generates (and mutates) PageMaker files for comparing `internal/pmd` with the
  C++ `pmd2json` it was ported from.
