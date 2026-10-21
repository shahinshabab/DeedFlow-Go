"""Runs admin scenarios (adding, changing, deleting through the forms) against
the Django admin and the Go admin, one after the other on the same database,
and compares what they answer and the rows they leave behind.

    admin_flow.py [scenario-name-prefix ...]
"""
import datetime
import difflib
import io
import json
import os
import re
import sys
import uuid

sys.path.insert(0, os.path.dirname(__file__))
from pages import Site, norm  # noqa: E402

import psycopg2  # noqa: E402
import psycopg2.extras  # noqa: E402

DB = dict(host="127.0.0.1", port=5433, dbname="deedflow", user="deedflow", password="deedflow")
DJ, GO = "http://127.0.0.1:8091", "http://127.0.0.1:8090"
MONTHS = r"(?:Jan|Feb|March|April|May|June|July|Aug|Sept|Oct|Nov|Dec)\.?"
DATE_RE = re.compile(MONTHS + r" \d+, 20\d\d, (?:midnight|noon|\d+(?::\d\d)? [ap]\.m\.)")


def multipart(fields, files):
    boundary = "----df" + uuid.uuid4().hex
    out = io.BytesIO()
    for name, value in fields:
        out.write(f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n'.encode())
    for name, filename, content, ctype in files:
        out.write(f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"; filename="{filename}"\r\n'
                  f'Content-Type: {ctype}\r\n\r\n'.encode())
        out.write(content + b"\r\n")
    out.write(f"--{boundary}--\r\n".encode())
    return out.getvalue(), f"multipart/form-data; boundary={boundary}"


def sql(query, args=None, fetch=True):
    with psycopg2.connect(**DB) as c, c.cursor(cursor_factory=psycopg2.extras.RealDictCursor) as cur:
        cur.execute(query, args)
        if fetch and cur.description:
            return [dict(r) for r in cur.fetchall()]
        return []


class Run:
    def __init__(self, site, label):
        self.site, self.label = site, label
        self.ids = {}
        self.log = []
        self.created = []  # (table, where, args) cleanups

    # --- fixtures -------------------------------------------------------------------------
    def fixture(self, name, query, args=()):
        rows = sql(query + " RETURNING id", args)
        self.ids[name] = rows[0]["id"]
        return rows[0]["id"]

    def mask(self, text):
        return text  # masking happens once the ids are all known (finish)

    def real_mask(self, text):
        if not isinstance(text, str):
            return text
        for name, i in sorted(self.ids.items(), key=lambda kv: -len(str(kv[1]))):
            text = re.sub(r"(?<![0-9])%s(?![0-9])" % re.escape(str(i)), "<%s>" % name, text)
        text = re.sub(r'name="csrfmiddlewaretoken" value="[^"]*"', 'name="csrfmiddlewaretoken" value="T"', text)
        text = DATE_RE.sub("<date>", text)
        return text

    # --- requests ---------------------------------------------------------------------------
    def get(self, path, name=None, body=False):
        s, h, b = self.site.req("GET", path)
        self.record(name or "GET " + path, s, h, b, body)
        return s, h, b

    def post(self, path, data, name=None, files=(), body=True, follow=True):
        if files or any(k == "__multipart__" for k in data):
            fields = [(k, v) for k, vs in data.items() for v in (vs if isinstance(vs, list) else [vs])]
            fields.append(("csrfmiddlewaretoken", self.site.csrf()))
            raw, ct = multipart(fields, files)
            s, h, b = self.site.req("POST", path, raw_body=raw, content_type=ct)
        else:
            s, h, b = self.site.req("POST", path, data)
        self.record(name or "POST " + path, s, h, b, body)
        if follow and s == 302 and h.get("Location"):
            loc = h.get("Location")
            if loc.startswith("http"):
                loc = loc[len(self.site.base):]
            s2, h2, b2 = self.site.req("GET", loc)
            self.record("  -> GET " + loc, s2, h2, b2, False, messages=True)
        return s, h, b

    def record(self, name, s, h, b, body, messages=False):
        entry = {"name": self.mask(name), "status": s, "location": self.mask(h.get("Location"))}
        if s == 200 and (body or messages):
            nodes = norm(re.sub(r"<!--.*?-->", "", b, flags=re.S))
            nodes = [self.mask(n) for n in nodes]
            if messages:
                nodes = self.messages(b)
            entry["page"] = nodes
        self.log.append(entry)

    def messages(self, html):
        out = re.findall(r'<li class="(?:success|error|warning|info)"[^>]*>(.*?)</li>', html, re.S)
        return [self.mask(re.sub(r"\s+", " ", m).strip()) for m in out]

    def snap(self, label, query, args=()):
        rows = sql(query, args)
        now = datetime.datetime.now(datetime.timezone.utc)
        out = []
        for r in rows:
            d = {}
            for k, v in r.items():
                if isinstance(v, datetime.datetime):
                    v = "<now>" if abs((now - v.astimezone(datetime.timezone.utc)).total_seconds()) < 600 else v.isoformat()
                elif isinstance(v, (datetime.date, uuid.UUID)):
                    v = str(v)
                elif isinstance(v, (bytes, memoryview)):
                    v = "<bytes>"
                elif isinstance(v, int) and not isinstance(v, bool):
                    if k == "id":
                        v = "<id>"
                    elif k.endswith("_id"):
                        names = [n for n, i in self.ids.items() if i == v]
                        v = "<%s>" % names[0] if names else "<id>"
                elif isinstance(v, str):
                    v = self.mask(v)
                    if re.fullmatch(r"[0-9a-f]{8}-[0-9a-f-]{27}", v):
                        v = "<uuid>"
                d[k] = v
            out.append(d)
        self.log.append({"name": "SNAP " + label, "rows": out})

    def finish(self):
        def walk(x):
            if isinstance(x, dict):
                return {k: walk(v) for k, v in x.items()}
            if isinstance(x, list):
                return [walk(v) for v in x]
            if isinstance(x, str):
                return self.real_mask(x)
            return x
        self.log = walk(self.log)

    def cleanup(self):
        for q, a in reversed(self.created):
            sql(q, a, fetch=False)


SCENARIOS = []


def scenario(fn):
    SCENARIOS.append(fn)
    return fn


def user_fixture(r, name, **kw):
    cols = dict(password="!x", last_login=None, is_superuser=False, username=name, first_name="", last_name="",
                email="", is_staff=False, is_active=True, date_joined=datetime.datetime(2020, 1, 1, tzinfo=datetime.timezone.utc))
    cols.update(kw)
    keys = list(cols)
    r.fixture(name, "INSERT INTO auth_user (%s) VALUES (%s)" % (",".join(keys), ",".join(["%s"] * len(keys))), [cols[k] for k in keys])
    r.created.append(("DELETE FROM auth_user WHERE id=%s", (r.ids[name],)))
    return r.ids[name]


def org_fixture(r, key, name):
    r.fixture(key, "INSERT INTO accounts_organization (name, created_at) VALUES (%s, now())", (name,))
    r.created.append(("DELETE FROM accounts_organization WHERE id=%s", (r.ids[key],)))
    return r.ids[key]


# --- scenarios ----------------------------------------------------------------------------------

@scenario
def org_add(r):
    r.get("/admin/accounts/organization/add/")
    r.post("/admin/accounts/organization/add/", {"name": ""}, "add empty")
    r.post("/admin/accounts/organization/add/", {"name": "x" * 300, "_save": "Save"}, "add too long")
    r.post("/admin/accounts/organization/add/", {"name": "  TwinOrg Add  ", "_save": "Save",
           "memberships-TOTAL_FORMS": "0", "memberships-INITIAL_FORMS": "0", "memberships-MIN_NUM_FORMS": "0", "memberships-MAX_NUM_FORMS": "1000"}, "add ok")
    rows = sql("SELECT id FROM accounts_organization WHERE name=%s", ("TwinOrg Add",))
    for x in rows:
        r.ids["neworg"] = x["id"]
        r.created.append(("DELETE FROM django_admin_log WHERE object_id=%s AND content_type_id=(SELECT id FROM django_content_type WHERE app_label='accounts' AND model='organization')", (str(x["id"]),)))
        r.created.append(("DELETE FROM accounts_organization WHERE id=%s", (x["id"],)))
    r.snap("orgs", "SELECT id, name, created_at FROM accounts_organization WHERE name LIKE 'TwinOrg%%' ORDER BY id")
    r.snap("log", "SELECT object_repr, action_flag, change_message FROM django_admin_log WHERE object_repr LIKE 'TwinOrg%%' ORDER BY id")


@scenario
def org_change_inlines(r):
    uid1 = user_fixture(r, "twin_u1")
    uid2 = user_fixture(r, "twin_u2")
    oid = org_fixture(r, "org", "TwinOrg Change")
    r.fixture("mem1", "INSERT INTO accounts_membership (user_id, organization_id, role, created_at) VALUES (%s, %s, 'staff', now())", (uid1, oid))
    mgmt = {"memberships-TOTAL_FORMS": "1", "memberships-INITIAL_FORMS": "1", "memberships-MIN_NUM_FORMS": "0", "memberships-MAX_NUM_FORMS": "1000"}
    base = dict(mgmt)
    base.update({"name": "TwinOrg Changed", "memberships-0-id": str(r.ids["mem1"]), "memberships-0-organization": str(oid),
                 "memberships-0-user": str(uid1), "memberships-0-role": "admin"})
    r.get(f"/admin/accounts/organization/{oid}/change/")
    r.post(f"/admin/accounts/organization/{oid}/change/", dict(base, **{"memberships-0-role": "bogus"}), "bad role")
    r.post(f"/admin/accounts/organization/{oid}/change/", dict(base, **{"memberships-0-user": "99999999"}), "bad user")
    r.post(f"/admin/accounts/organization/{oid}/change/", dict(base, **{"_continue": "1"}), "change role continue")
    add = dict(base)
    add.update({"memberships-TOTAL_FORMS": "2", "memberships-1-organization": str(oid), "memberships-1-user": str(uid2), "memberships-1-role": "staff"})
    r.post(f"/admin/accounts/organization/{oid}/change/", add, "add membership")
    dele = dict(base)
    dele.update({"memberships-0-DELETE": "on", "_addanother": "1"})
    r.post(f"/admin/accounts/organization/{oid}/change/", dele, "delete membership + addanother")
    r.snap("org", "SELECT id, name FROM accounts_organization WHERE id=%s", (oid,))
    r.snap("members", "SELECT id, user_id, organization_id, role FROM accounts_membership WHERE organization_id=%s ORDER BY id", (oid,))
    r.snap("log", "SELECT object_repr, action_flag, change_message FROM django_admin_log WHERE object_id=%s AND content_type_id=(SELECT id FROM django_content_type WHERE app_label='accounts' AND model='organization') ORDER BY id", (str(oid),))
    r.get(f"/admin/accounts/organization/{oid}/history/", "history", body=True)
    r.created.append(("DELETE FROM django_admin_log WHERE object_id=%s AND content_type_id=(SELECT id FROM django_content_type WHERE app_label='accounts' AND model='organization')", (str(oid),)))
    r.created.append(("DELETE FROM accounts_membership WHERE organization_id=%s", (oid,)))


def run_all(prefixes):
    results = {}
    for fn in SCENARIOS:
        if prefixes and not any(fn.__name__.startswith(p) for p in prefixes):
            continue
        logs = {}
        for label, base in (("django", DJ), ("go", GO)):
            site = Site(base, "admin", "correct-horse-7")
            site.login()
            run = Run(site, label)
            try:
                fn(run)
            finally:
                run.cleanup()
            run.finish()
            logs[label] = run.log
        results[fn.__name__] = logs
    return results


def main():
    results = run_all(sys.argv[1:])
    bad = 0
    for name, logs in results.items():
        a = json.dumps(logs["django"], indent=1, sort_keys=True, default=str, ensure_ascii=False).splitlines()
        b = json.dumps(logs["go"], indent=1, sort_keys=True, default=str, ensure_ascii=False).splitlines()
        if a == b:
            print(f"{name}: identical ({len(logs['django'])} steps)")
            continue
        bad += 1
        print(f"{name}: DIFFERS")
        for line in list(difflib.unified_diff(a, b, "django", "go", lineterm="", n=3))[:120]:
            print("   ", line)
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
