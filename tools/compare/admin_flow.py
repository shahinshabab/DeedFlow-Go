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


TABLES = ["auth_group", "auth_user", "accounts_organization", "accounts_membership", "accounts_portalcredential", "deeds_deed",
          "deeds_registrationdetail", "deeds_presenter", "deeds_priordocument", "deeds_executant", "deeds_claimant",
          "deeds_scheduleitem", "deeds_witness", "deeds_deedscreenshot", "ledger_registeredtoken", "ledger_paymentreceipt",
          "ledger_receiptsyncrun", "ledger_expense", "ledger_expensebill", "auth_group_permissions", "auth_user_groups",
          "auth_user_user_permissions"]


def bump_sequences(offset):
    """Ids far from every small number, so that masking them cannot hit anything else."""
    for n, t in enumerate(TABLES):
        sql("SELECT setval(pg_get_serial_sequence(%s, 'id'), GREATEST((SELECT last_value FROM " + t + "_id_seq), %s))", (t, 700000 + 1000 * n + offset), fetch=False)


class Run:
    def __init__(self, site, label):
        self.site, self.label = site, label
        bump_sequences(0 if label == "django" else 20000)
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
        text = re.sub(r"object \(\d+\)", "object (N)", text)
        return text

    # --- requests ---------------------------------------------------------------------------
    def get(self, path, name=None, body=False):
        s, h, b = self.site.req("GET", path)
        self.record(name or "GET " + path, s, h, b, body)
        return s, h, b

    def post(self, path, data, name=None, files=(), body=True, follow=True, brief=None):
        if files or any(k == "__multipart__" for k in data):
            fields = [(k, v) for k, vs in data.items() for v in (vs if isinstance(vs, list) else [vs])]
            fields.append(("csrfmiddlewaretoken", self.site.csrf()))
            raw, ct = multipart(fields, files)
            s, h, b = self.site.req("POST", path, raw_body=raw, content_type=ct)
        else:
            s, h, b = self.site.req("POST", path, data)
        if brief and s == 200:
            self.brief(name or "POST " + path, s, b, brief)
            return s, h, b
        self.record(name or "POST " + path, s, h, b, body)
        if follow and s == 302 and h.get("Location"):
            loc = h.get("Location")
            if loc.startswith("http"):
                loc = loc[len(self.site.base):]
            s2, h2, b2 = self.site.req("GET", loc)
            self.record("  -> GET " + loc, s2, h2, b2, False, messages=True)
        return s, h, b

    def brief(self, name, s, b, field):
        errs = [re.sub(r"\s+", " ", re.sub("<[^>]+>", " ", m)).strip() for m in re.findall(r'<ul class="errorlist[^"]*">(.*?)</ul>', b, re.S)]
        widget = re.findall(r'<(?:input|select|textarea)[^>]*\bname="%s(?:_\d)?"[^>]*>' % re.escape(field), b)
        self.log.append({"name": name, "status": s, "errors": errs, "widget": sorted(widget)})

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
            try:
                sql(q, a, fetch=False)
            except Exception as e:  # noqa: BLE001
                print("cleanup failed:", q[:60], e.__class__.__name__)


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



def log_cleanup(r, label, pk):
    r.created.append(("DELETE FROM django_admin_log WHERE object_id=%s AND content_type_id=(SELECT id FROM django_content_type WHERE app_label=%s AND model=%s)", (str(pk), *label.split("."))))


def find_and_clean(r, table, where, args, key, label=None):
    rows = sql(f"SELECT id FROM {table} WHERE {where} ORDER BY id", args)
    for n, x in enumerate(rows):
        r.ids[key if n == len(rows) - 1 else f"{key}_{n}"] = x["id"]
        if label:
            log_cleanup(r, label, x["id"])
        r.created.append((f"DELETE FROM {table} WHERE id=%s", (x["id"],)))
    return [x["id"] for x in rows]


@scenario
def group_flow(r):
    r.get("/admin/auth/group/add/")
    r.post("/admin/auth/group/add/", {"name": "", "permissions": []}, "empty")
    r.post("/admin/auth/group/add/", {"name": "x" * 151}, "too long")
    r.post("/admin/auth/group/add/", {"name": "TwinGroup", "permissions": ["999999"]}, "bad perm")
    r.post("/admin/auth/group/add/", {"name": "TwinGroup", "permissions": ["abc"]}, "bad perm text")
    perms = [x["id"] for x in sql("SELECT id FROM auth_permission WHERE codename IN ('add_deed','view_deed') ORDER BY id")]
    r.post("/admin/auth/group/add/", {"name": "TwinGroup", "permissions": [str(p) for p in perms], "_continue": "1"}, "ok")
    gids = find_and_clean(r, "auth_group", "name=%s", ("TwinGroup",), "grp", "auth.group")
    r.created.append(("DELETE FROM auth_group_permissions WHERE group_id=%s", (gids[0] if gids else 0,)))
    r.post("/admin/auth/group/add/", {"name": "TwinGroup"}, "duplicate")
    gid = r.ids.get("grp", 0)
    r.post(f"/admin/auth/group/{gid}/change/", {"name": "TwinGroup2", "permissions": [str(perms[0])]}, "change")
    r.snap("group", "SELECT id, name FROM auth_group WHERE name LIKE 'TwinGroup%%'")
    r.snap("gperms", "SELECT group_id, permission_id FROM auth_group_permissions WHERE group_id=%s ORDER BY permission_id", (gid,))
    r.snap("log", "SELECT object_repr, action_flag, change_message FROM django_admin_log WHERE object_repr LIKE 'TwinGroup%%' ORDER BY id")
    r.get(f"/admin/auth/group/{gid}/history/", "history", body=True)
    r.get(f"/admin/auth/group/{gid}/delete/", "delete page", body=True)
    r.post(f"/admin/auth/group/{gid}/delete/", {"post": "yes"}, "delete")
    r.snap("after delete", "SELECT id FROM auth_group WHERE name LIKE 'TwinGroup%%'")


@scenario
def user_add_flow(r):
    pw = "Tw1n-Passw0rd-xyz"
    base = {"username": "twin_new", "usable_password": "true", "password1": pw, "password2": pw}
    r.post("/admin/auth/user/add/", {"username": "", "usable_password": "true", "password1": "", "password2": ""}, "empty")
    r.post("/admin/auth/user/add/", dict(base, password2="different"), "mismatch")
    r.post("/admin/auth/user/add/", dict(base, password1="abc", password2="abc"), "weak")
    r.post("/admin/auth/user/add/", dict(base, password1="12345678", password2="12345678"), "numeric")
    r.post("/admin/auth/user/add/", dict(base, username="bad name!"), "bad username")
    r.post("/admin/auth/user/add/", dict(base, username="ADMIN"), "case dup")
    r.post("/admin/auth/user/add/", dict(base, username="x" * 151), "long username")
    r.post("/admin/auth/user/add/", dict(base, password1="twin_new123", password2="twin_new123"), "similar")
    r.post("/admin/auth/user/add/", dict(base), "ok")
    find_and_clean(r, "auth_user", "username=%s", ("twin_new",), "newuser", "auth.user")
    r.post("/admin/auth/user/add/", dict(base, username="twin_new2", usable_password="false", password1="", password2="", _addanother="1"), "unusable + addanother")
    find_and_clean(r, "auth_user", "username=%s", ("twin_new2",), "newuser2", "auth.user")
    r.snap("users", "SELECT id, username, is_staff, is_superuser, is_active, first_name, last_name, email, (password LIKE 'pbkdf2_sha256$%%') AS hashed, (password LIKE '!%%') AS unusable, date_joined, last_login FROM auth_user WHERE username LIKE 'twin_new%%' ORDER BY username")
    r.snap("log", "SELECT object_repr, action_flag, change_message FROM django_admin_log WHERE object_repr LIKE 'twin_new%%' ORDER BY id")


@scenario
def user_change_flow(r):
    uid = user_fixture(r, "twin_chg", email="a@example.com", password="pbkdf2_sha256$870000$saltsaltsalt$aGFzaGhhc2hoYXNoaGFzaGhhc2g=")
    gid = r.fixture("tg", "INSERT INTO auth_group (name) VALUES ('TwinGrpU')")
    r.created.append(("DELETE FROM auth_group WHERE id=%s", (gid,)))
    r.created.append(("DELETE FROM auth_user_groups WHERE user_id=%s", (uid,)))
    r.created.append(("DELETE FROM auth_user_user_permissions WHERE user_id=%s", (uid,)))
    log_cleanup(r, "auth.user", uid)
    path = f"/admin/auth/user/{uid}/change/"
    base = {"username": "twin_chg", "first_name": "Tw", "last_name": "In", "email": "a@example.com", "is_active": "on",
            "date_joined_0": "2020-01-01", "date_joined_1": "05:30:00", "last_login_0": "", "last_login_1": "",
            "initial-date_joined_0": "2020-01-01", "initial-date_joined_1": "05:30:00"}
    r.get(path)
    r.post(path, dict(base, email="not-an-email"), "bad email")
    r.post(path, dict(base, username="admin"), "dup username")
    r.post(path, dict(base, date_joined_0="notadate"), "bad date")
    r.post(path, dict(base, date_joined_0="", date_joined_1=""), "no date_joined")
    r.post(path, dict(base, last_login_0="2021-02-03", last_login_1=""), "partial last_login")
    r.post(path, dict(base, groups=["9999999"]), "bad group")
    r.post(path, dict(base, is_staff="on", groups=[str(gid)], user_permissions=[str(x["id"]) for x in sql("SELECT id FROM auth_permission WHERE codename='view_deed'")],
                      last_login_0="2021-02-03", last_login_1="10:11:12", first_name="Changed"), "ok")
    r.post(path, dict(base, is_staff="on", is_superuser="on", _continue="1"), "ok2 continue")
    r.snap("user", "SELECT id, username, first_name, last_name, email, is_staff, is_superuser, is_active, date_joined, last_login, password FROM auth_user WHERE id=%s", (uid,))
    r.snap("groups", "SELECT group_id FROM auth_user_groups WHERE user_id=%s", (uid,))
    r.snap("uperms", "SELECT permission_id FROM auth_user_user_permissions WHERE user_id=%s", (uid,))
    r.snap("log", "SELECT object_repr, action_flag, change_message FROM django_admin_log WHERE object_id=%s AND content_type_id=(SELECT id FROM django_content_type WHERE app_label='auth' AND model='user') ORDER BY id", (str(uid),))
    r.get(path, "page after", body=True)


@scenario
def user_password_flow(r):
    uid = user_fixture(r, "twin_pw", password="pbkdf2_sha256$870000$saltsaltsalt$aGFzaGhhc2hoYXNoaGFzaGhhc2g=")
    log_cleanup(r, "auth.user", uid)
    path = f"/admin/auth/user/{uid}/password/"
    r.get(path, body=True)
    pw = "Tw1n-Passw0rd-xyz"
    r.post(path, {"usable_password": "true", "password1": "", "password2": ""}, "empty")
    r.post(path, {"usable_password": "true", "password1": pw, "password2": "x"}, "mismatch")
    r.post(path, {"usable_password": "true", "password1": "abc", "password2": "abc"}, "weak")
    r.post(path, {"usable_password": "false", "password1": "", "password2": "", "set-password": "Change"}, "conflict")
    r.post(path, {"usable_password": "true", "password1": pw, "password2": pw, "set-password": "Change"}, "ok")
    r.snap("pw", "SELECT (password LIKE 'pbkdf2_sha256$%%') AS hashed FROM auth_user WHERE id=%s", (uid,))
    r.post(path, {"usable_password": "false", "password1": "", "password2": "", "unset-password": "x"}, "unset")
    r.snap("pw2", "SELECT (password LIKE '!%%') AS unusable FROM auth_user WHERE id=%s", (uid,))
    r.get(path, "page after unset", body=True)
    r.post(path, {"password1": pw, "password2": pw, "set-password": "Enable"}, "enable again")
    r.snap("log", "SELECT object_repr, action_flag, change_message FROM django_admin_log WHERE object_id=%s AND content_type_id=(SELECT id FROM django_content_type WHERE app_label='auth' AND model='user') ORDER BY id", (str(uid),))


@scenario
def credential_flow(r):
    oid = org_fixture(r, "corg", "TwinOrg Cred")
    oid2 = org_fixture(r, "corg2", "TwinOrg Cred2")
    r.get("/admin/accounts/portalcredential/add/")
    base = {"organization": str(oid), "portal_username": "u1", "new_deed_menu_index": "1", "my_deeds_menu_index": "2", "new_password": ""}
    r.post("/admin/accounts/portalcredential/add/", dict(base, organization=""), "no org")
    r.post("/admin/accounts/portalcredential/add/", dict(base, new_deed_menu_index="-1", my_deeds_menu_index="99999"), "bad ints")
    r.post("/admin/accounts/portalcredential/add/", dict(base, new_deed_menu_index="x"), "bad int text")
    r.post("/admin/accounts/portalcredential/add/", dict(base, portal_username="y" * 300), "long user")
    r.post("/admin/accounts/portalcredential/add/", dict(base, new_password="secret-1", _continue="1"), "ok")
    ids = find_and_clean(r, "accounts_portalcredential", "organization_id=%s", (oid,), "cred", "accounts.portalcredential")
    r.post("/admin/accounts/portalcredential/add/", dict(base), "duplicate org")
    cid = r.ids.get("cred", 0)
    r.snap("cred", "SELECT id, organization_id, portal_username, (portal_password <> '') AS has_pw, new_deed_menu_index, my_deeds_menu_index, collect_receipts, alert_email, updated_at FROM accounts_portalcredential WHERE id=%s", (cid,))
    before = sql("SELECT portal_password FROM accounts_portalcredential WHERE id=%s", (cid,))
    r.post(f"/admin/accounts/portalcredential/{cid}/change/", dict(base, portal_username="u2"), "change keep pw")
    after = sql("SELECT portal_password FROM accounts_portalcredential WHERE id=%s", (cid,))
    r.log.append({"name": "SNAP pw unchanged", "same": bool(before and after and before[0]["portal_password"] == after[0]["portal_password"])})
    r.post(f"/admin/accounts/portalcredential/{cid}/change/", dict(base, organization=str(oid2), new_password="secret-2"), "change org+pw")
    r.snap("cred2", "SELECT id, organization_id, portal_username, (portal_password <> '') AS has_pw FROM accounts_portalcredential WHERE id=%s", (cid,))
    r.snap("log", "SELECT object_repr, action_flag, change_message FROM django_admin_log WHERE object_id=%s AND content_type_id=(SELECT id FROM django_content_type WHERE app_label='accounts' AND model='portalcredential') ORDER BY id", (str(cid),))



def deed_base(owner, **kw):
    d = {"owner": str(owner), "organization": "", "title": "Twin deed", "status": "draft", "progress": "0", "token_number": "",
         "extracted_text": "", "ai_raw_response": "", "result_message": "", "portal_snapshot": "{}", "survey_mapping": "[]",
         "initial-portal_snapshot": "{}", "initial-survey_mapping": "[]"}
    d.update(kw)
    return d


def inline_mgmt(prefix, total=0, initial=0, mx=1000):
    return {f"{prefix}-TOTAL_FORMS": str(total), f"{prefix}-INITIAL_FORMS": str(initial), f"{prefix}-MIN_NUM_FORMS": "0", f"{prefix}-MAX_NUM_FORMS": str(mx)}


DEED_INLINES = [("registration", 1), ("presenter", 1), ("prior_documents", 1000), ("executants", 1000), ("claimants", 1000),
                ("schedule_items", 1000), ("witnesses", 1000), ("screenshots", 1000)]


def deed_mgmt(**over):
    d = {}
    for p, mx in DEED_INLINES:
        d.update(inline_mgmt(p, mx=mx))
    d.update(over)
    return d


@scenario
def deed_flow(r):
    uid = user_fixture(r, "twin_owner")
    oid = org_fixture(r, "dorg", "TwinOrg Deed")
    r.fixture("dmem", "INSERT INTO accounts_membership (user_id, organization_id, role, created_at) VALUES (%s, %s, 'staff', now())", (uid, oid))
    r.created.append(("DELETE FROM accounts_membership WHERE id=%s", (r.ids["dmem"],)))
    r.get("/admin/deeds/deed/add/")
    mg = deed_mgmt()
    path = "/admin/deeds/deed/add/"
    r.post(path, dict(deed_base(uid), **mg, owner=""), "no owner")
    r.post(path, dict(deed_base(uid), **mg, status="bogus"), "bad status")
    r.post(path, dict(deed_base(uid), **mg, progress="40000"), "progress too big")
    r.post(path, dict(deed_base(uid), **mg, progress="-5"), "progress negative")
    r.post(path, dict(deed_base(uid), **mg, ai_raw_response="{not json"), "bad json")
    r.post(path, dict(deed_base(uid), **mg, ai_raw_response='{"a": [1, 2]}', survey_mapping='[1,2,3]', title="Twin deed"), "ok", )
    ids = find_and_clean(r, "deeds_deed", "title=%s", ("Twin deed",), "deed", "deeds.deed")
    did = r.ids.get("deed", 0)
    r.created.append(("DELETE FROM deeds_executant WHERE deed_id=%s", (did,)))
    r.created.append(("DELETE FROM deeds_registrationdetail WHERE deed_id=%s", (did,)))
    r.snap("deed", "SELECT id, owner_id, organization_id, title, status, progress, token_number, is_test, ai_raw_response, portal_snapshot, survey_mapping, extracted_text, result_message FROM deeds_deed WHERE id=%s", (did,))
    path = f"/admin/deeds/deed/{did}/change/"
    mg = deed_mgmt(**{"registration-TOTAL_FORMS": "1", "executants-TOTAL_FORMS": "1"})
    data = dict(deed_base(uid, organization=str(oid), ai_raw_response='{"a": [1, 2]}', survey_mapping="[1,2,3]"), **mg)
    data.update({"registration-0-deed": str(did), "registration-0-district": "D", "registration-0-sro": "S", "registration-0-taluk": "T",
                 "registration-0-book": "1", "registration-0-deed_type": "X", "registration-0-deed_sub_type": "",
                 "executants-0-deed": str(did), "executants-0-name": "Exec One", "executants-0-capacity": "", "executants-0-age": "30"})
    r.post(path, dict(data, **{"registration-0-book": "7"}), "bad book")
    r.post(path, dict(data, **{"registration-0-district": ""}), "missing district")
    r.post(path, dict(data, **{"executants-0-age": "abc"}), "bad age")
    r.post(path, dict(data, **{"_continue": "1"}), "add inlines")
    r.snap("registration", "SELECT id, deed_id, district, sro, taluk, book, deed_type, deed_sub_type FROM deeds_registrationdetail WHERE deed_id=%s", (did,))
    r.snap("executants", "SELECT id, deed_id, name, age, capacity, position FROM deeds_executant WHERE deed_id=%s ORDER BY id", (did,))
    rid = sql("SELECT id FROM deeds_registrationdetail WHERE deed_id=%s", (did,))
    eid = sql("SELECT id FROM deeds_executant WHERE deed_id=%s", (did,))
    if rid and eid:
        r.ids["reg"], r.ids["exe"] = rid[0]["id"], eid[0]["id"]
        data2 = dict(deed_base(uid, organization=str(oid), ai_raw_response='{"a": [1, 2]}', survey_mapping="[1,2,3]"), **deed_mgmt(**{"registration-TOTAL_FORMS": "1", "registration-INITIAL_FORMS": "1", "executants-TOTAL_FORMS": "1", "executants-INITIAL_FORMS": "1"}))
        data2.update({"registration-0-id": str(rid[0]["id"]), "registration-0-deed": str(did), "registration-0-district": "D2", "registration-0-sro": "S", "registration-0-taluk": "T",
                      "registration-0-book": "3", "registration-0-deed_type": "X", "registration-0-deed_sub_type": "",
                      "executants-0-id": str(eid[0]["id"]), "executants-0-deed": str(did), "executants-0-name": "Exec Renamed", "executants-0-age": "31", "executants-0-DELETE": ""})
        r.post(path, dict(data2, title="Twin deed renamed", status="completed"), "change inlines")
        r.post(path, dict(data2, **{"executants-0-DELETE": "on", "registration-0-DELETE": "on"}), "delete inlines")
    r.snap("registration after", "SELECT id, deed_id, district FROM deeds_registrationdetail WHERE deed_id=%s", (did,))
    r.snap("executants after", "SELECT id, deed_id, name FROM deeds_executant WHERE deed_id=%s ORDER BY id", (did,))
    r.snap("deed after", "SELECT id, title, status, organization_id FROM deeds_deed WHERE id=%s", (did,))
    r.snap("log", "SELECT object_repr, action_flag, change_message FROM django_admin_log WHERE object_id=%s AND content_type_id=(SELECT id FROM django_content_type WHERE app_label='deeds' AND model='deed') ORDER BY id", (str(did),))
    r.get(path, "page after", body=True)
    r.get(f"/admin/deeds/deed/{did}/delete/", "delete page", body=True)
    r.post(f"/admin/deeds/deed/{did}/delete/", {"post": "yes"}, "delete")
    r.snap("deed gone", "SELECT id FROM deeds_deed WHERE id=%s", (did,))


@scenario
def token_flow(r):
    oid = org_fixture(r, "torg", "TwinOrg Tok")
    path = "/admin/ledger/registeredtoken/add/"
    mg = inline_mgmt("receipts")
    base = {"organization": str(oid), "token": "TTWIN1", "presenter_name_ml": "", "presentation_date": "2026-01-02", "presenter_place_ml": "",
            "presenter_address_ml": "", "presenter_city_ml": "", "presented_at_0": "", "presented_at_1": "", "document_amount": "12.5",
            "applicant_name": "App", "sro": "SRO1", "status": "pending", "attempts": "0", "last_error": "", "collected_at_0": "", "collected_at_1": ""}
    r.get(path)
    r.post(path, dict(base, **mg, token=""), "no token")
    r.post(path, dict(base, **mg, presentation_date="02/31/2026"), "bad date")
    r.post(path, dict(base, **mg, presentation_date="01/02/2026"), "us date ok?")
    find_and_clean(r, "ledger_registeredtoken", "token=%s", ("TTWIN1",), "tok1", "ledger.registeredtoken")
    r.post(path, dict(base, **mg, document_amount="abc"), "bad decimal")
    r.post(path, dict(base, **mg, document_amount="123456789012"), "decimal too many digits")
    r.post(path, dict(base, **mg, document_amount="1.234"), "decimal places")
    r.post(path, dict(base, **mg, presented_at_0="2026-01-01", presented_at_1=""), "datetime time missing")
    r.post(path, dict(base, **mg, presented_at_0="2026-01-01", presented_at_1="25:00"), "bad time")
    r.post(path, dict(base, **mg, attempts="40000"), "attempts big")
    r.post(path, dict(base, **mg, status="nope"), "bad status")
    r.post(path, dict(base, **mg, token="TTWIN2", presented_at_0="2026-01-01", presented_at_1="10:20:30", _continue="1"), "ok datetime")
    find_and_clean(r, "ledger_registeredtoken", "token=%s", ("TTWIN2",), "tok2", "ledger.registeredtoken")
    r.post(path, dict(base, **mg, token="TTWIN2"), "duplicate token")
    tid = r.ids.get("tok2", 0)
    r.snap("tokens", "SELECT id, organization_id, token, presentation_date, presented_at, document_amount, applicant_name, sro, status, attempts, last_error, collected_at FROM ledger_registeredtoken WHERE token LIKE 'TTWIN%%' ORDER BY token")
    cpath = f"/admin/ledger/registeredtoken/{tid}/change/"
    data = dict(base, **inline_mgmt("receipts", 1, 0), token="TTWIN2")
    data.update({"receipts-0-token": str(tid), "receipts-0-kind": "stamp", "receipts-0-transaction_no": "TX1", "receipts-0-amount": "100.00", "receipts-0-grn": "G1", "receipts-0-paid_at_0": "2026-01-03", "receipts-0-paid_at_1": "09:00:00", "receipts-0-status": "paid"})
    r.post(cpath, data, "add receipt")
    r.post(cpath, dict(data, **{"receipts-0-kind": "zzz"}), "bad kind")
    r.post(cpath, dict(data, **{"receipts-0-amount": "x"}), "bad amount")
    r.snap("receipts", "SELECT id, token_id, kind, transaction_no, amount, grn, paid_at, status, fields FROM ledger_paymentreceipt WHERE token_id=%s ORDER BY id", (tid,))
    r.created.append(("DELETE FROM ledger_paymentreceipt WHERE token_id=%s", (tid,)))
    r.get(cpath, "after", body=True)


@scenario
def run_expense_flow(r):
    oid = org_fixture(r, "rorg", "TwinOrg Run")
    uid = user_fixture(r, "twin_creator")
    path = "/admin/ledger/receiptsyncrun/add/"
    base = {"organization": str(oid), "attempt": "1", "status": "partial", "finished_at_0": "", "finished_at_1": "", "registered_found": "3", "collected": "2", "pending": "1", "message": "", "alert_sent_to": ""}
    r.post(path, dict(base, organization=""), "no org")
    r.post(path, dict(base, attempt="x"), "bad attempt")
    r.post(path, dict(base, finished_at_0="2026-02-02", finished_at_1="12:00:00"), "ok")
    find_and_clean(r, "ledger_receiptsyncrun", "organization_id=%s", (oid,), "run", "ledger.receiptsyncrun")
    r.snap("run", "SELECT id, organization_id, attempt, status, finished_at, registered_found, collected, pending, started_at FROM ledger_receiptsyncrun WHERE organization_id=%s", (oid,))
    epath = "/admin/ledger/expense/add/"
    ebase = dict({"organization": str(oid), "date": "2026-03-04", "category": "rent", "amount": "1500.50", "payment_mode": "cash", "description": "Twin expense", "created_by": str(uid)}, **inline_mgmt("bills"))
    r.post(epath, dict(ebase, date=""), "no date")
    r.post(epath, dict(ebase, amount="-1"), "negative amount")
    r.post(epath, dict(ebase, category="zzz"), "bad category")
    r.post(epath, dict(ebase, created_by="999999999"), "bad creator")
    r.post(epath, dict(ebase, description="x" * 300), "long description")
    r.post(epath, dict(ebase, **{"bills-TOTAL_FORMS": "1", "bills-0-original_name": "b.pdf"}), "bill without file")
    r.post(epath, dict(ebase, **{"bills-TOTAL_FORMS": "1", "bills-0-original_name": "b.pdf", "__multipart__": "1"}), "bill with file",
           files=[("bills-0-file", "my bill.pdf", b"%PDF-1.4 twin", "application/pdf")])
    find_and_clean(r, "ledger_expense", "description=%s", ("Twin expense",), "exp", "ledger.expense")
    eid = r.ids.get("exp", 0)
    r.created.append(("DELETE FROM ledger_expensebill WHERE expense_id=%s", (eid,)))
    for b in sql("SELECT id FROM ledger_expensebill WHERE expense_id=%s ORDER BY id", (eid,)):
        r.ids["bill%d" % len([k for k in r.ids if k.startswith("bill")])] = b["id"]
    r.snap("expense", "SELECT id, organization_id, date, category, amount, payment_mode, description, created_by_id FROM ledger_expense WHERE id=%s", (eid,))
    r.snap("bills", "SELECT id, expense_id, original_name, regexp_replace(file, '[0-9a-zA-Z]{7}(\\.[a-z]+)$', 'XXXXXXX\\1') AS file FROM ledger_expensebill WHERE expense_id=%s ORDER BY id", (eid,))
    r.get(f"/admin/ledger/expense/{eid}/change/", "page", body=True)
    r.post(f"/admin/ledger/expense/{eid}/delete/", {"post": "yes"}, "delete")
    r.snap("gone", "SELECT id FROM ledger_expense WHERE id=%s", (eid,))
    r.snap("bills gone", "SELECT id FROM ledger_expensebill WHERE expense_id=%s", (eid,))


@scenario
def action_flow(r):
    oid = org_fixture(r, "aorg1", "TwinOrg Act1")
    oid2 = org_fixture(r, "aorg2", "TwinOrg Act2")
    org_fixture(r, "aorg3", "TwinOrg Act3")
    path = "/admin/accounts/organization/"
    r.get(path + "?q=TwinOrg%20Act", "list", body=True)
    r.post(path, {"action": "delete_selected", "index": "0", "select_across": "0"}, "no selection")
    r.post(path, {"action": "", "index": "0", "select_across": "0", "_selected_action": [str(oid)]}, "no action")
    r.post(path, {"action": "delete_selected", "index": "0", "select_across": "0", "_selected_action": [str(oid), str(oid2)]}, "confirm page")
    r.post(path, {"action": "delete_selected", "index": "0", "select_across": "0", "post": "yes", "_selected_action": [str(oid), str(oid2)]}, "delete two")
    r.snap("left", "SELECT id, name FROM accounts_organization WHERE name LIKE 'TwinOrg Act%%' ORDER BY id")
    r.post(path + "?q=TwinOrg%20Act", {"action": "delete_selected", "index": "0", "select_across": "1", "post": "yes", "_selected_action": ["1"]}, "across with filter")
    r.snap("left2", "SELECT id, name FROM accounts_organization WHERE name LIKE 'TwinOrg Act%%' ORDER BY id")
    r.snap("log", "SELECT object_repr, action_flag, change_message FROM django_admin_log WHERE object_repr LIKE 'TwinOrg Act%%' ORDER BY object_repr")
    r.created.append(("DELETE FROM django_admin_log WHERE object_repr LIKE 'TwinOrg Act%%'", ()))



def pbkdf2(password, iterations=870000):
    import base64
    import hashlib
    salt = "twinsalt12345"
    h = hashlib.pbkdf2_hmac("sha256", password.encode(), salt.encode(), iterations)
    return "pbkdf2_sha256$%d$%s$%s" % (iterations, salt, base64.b64encode(h).decode())


@scenario
def perms_flow(r):
    pw = "pw-Twin-staff-1"
    uid = user_fixture(r, "twin_staff", password=pbkdf2(pw), is_staff=True)
    r.created.append(("DELETE FROM auth_user_user_permissions WHERE user_id=%s", (uid,)))
    for code in ("view_deed", "change_organization", "add_organization", "view_organization", "delete_expense", "view_expense", "view_user", "change_user"):
        sql("INSERT INTO auth_user_user_permissions (user_id, permission_id) SELECT %s, id FROM auth_permission WHERE codename=%s", (uid, code), fetch=False)
    oid = org_fixture(r, "porg", "TwinOrg Perm")
    staff = Site(r.site.base, "twin_staff", pw)
    staff.login()
    r.site = staff
    for path in ["/admin/", "/admin/accounts/", "/admin/deeds/", "/admin/deeds/deed/", "/admin/deeds/deed/add/", "/admin/deeds/deed/1006/change/",
                 "/admin/deeds/deed/1006/history/", "/admin/deeds/deed/1006/delete/", "/admin/accounts/organization/", "/admin/accounts/organization/add/",
                 f"/admin/accounts/organization/{oid}/change/", f"/admin/accounts/organization/{oid}/delete/", "/admin/ledger/expense/",
                 "/admin/ledger/expense/1/delete/", "/admin/ledger/expense/1/change/", "/admin/auth/user/", "/admin/auth/user/add/", "/admin/auth/group/",
                 "/admin/auth/user/1/password/", "/admin/accounts/portalcredential/", "/admin/nope/", "/admin/accounts/nope/",
                 "/admin/accounts/organization/99999999/change/", "/admin/accounts/organization/?e=1", "/admin/accounts/organization/?name__icontains=x"]:
        r.get(path, body=True)
    base = dict({"name": "TwinOrg Perm Changed"}, **inline_mgmt("memberships"))
    r.post(f"/admin/accounts/organization/{oid}/change/", base, "change as staff")
    r.post("/admin/accounts/organization/add/", dict(base, name="TwinOrg Perm Added"), "add as staff")
    find_and_clean(r, "accounts_organization", "name=%s", ("TwinOrg Perm Added",), "padd", "accounts.organization")
    r.post("/admin/deeds/deed/1006/change/", {"title": "x"}, "change deed denied")
    r.post("/admin/ledger/expense/", {"action": "delete_selected", "index": "0", "select_across": "0", "_selected_action": ["999999"]}, "action on nothing")
    r.snap("orgs", "SELECT id, name FROM accounts_organization WHERE name LIKE 'TwinOrg Perm%%' ORDER BY id")
    r.created.append(("DELETE FROM django_admin_log WHERE user_id=%s", (uid,)))
    r.site = Site(r.site.base, "admin", "correct-horse-7")
    r.site.login()


@scenario
def popup_and_misc_flow(r):
    oid = org_fixture(r, "morg", "TwinOrg Misc")
    for path in ["/admin/accounts/organization/?_popup=1", "/admin/accounts/organization/?_popup=1&_to_field=id&q=TwinOrg",
                 "/admin/accounts/organization/add/?_popup=1&_to_field=id", f"/admin/accounts/organization/{oid}/change/?_popup=1&_to_field=id",
                 f"/admin/accounts/organization/{oid}/delete/?_popup=1&_to_field=id",
                 f"/admin/accounts/organization/{oid}/change/?_changelist_filters=q%3DTwin%26o%3D1", f"/admin/accounts/organization/{oid}/history/?_changelist_filters=q%3DTwin",
                 "/admin/accounts/organization/add/?name=Prefilled", f"/admin/accounts/organization/{oid}/", f"/admin/accounts/organization/{oid}",
                 "/admin/accounts/organization/abc/change/", "/admin/auth/user/add/?_popup=1", f"/admin/r/1/{oid}/", "/admin/r/9999/1/",
                 "/admin/jsi18n/", "/admin/password_change/", "/admin/password_change/done/"]:
        r.get(path, body=True)
    base = dict({"name": "TwinOrg Misc Pop", "_popup": "1", "_to_field": "id"}, **inline_mgmt("memberships"))
    r.post("/admin/accounts/organization/add/?_popup=1&_to_field=id", base, "popup add")
    find_and_clean(r, "accounts_organization", "name=%s", ("TwinOrg Misc Pop",), "mpop", "accounts.organization")
    r.post(f"/admin/accounts/organization/{oid}/change/?_popup=1&_to_field=id", dict(base, name="TwinOrg Misc Pop2"), "popup change")
    r.post(f"/admin/accounts/organization/{oid}/delete/?_popup=1", {"post": "yes", "_popup": "1"}, "popup delete")
    r.post("/admin/accounts/organization/add/?_changelist_filters=q%3DTwin", dict(base, name="TwinOrg Misc F1", _popup="", _to_field=""), "preserved filters add")
    find_and_clean(r, "accounts_organization", "name=%s", ("TwinOrg Misc F1",), "mf1", "accounts.organization")
    r.created.append(("DELETE FROM django_admin_log WHERE object_repr LIKE 'TwinOrg Misc%%'", ()))



FUZZ_INTS = ["", "5", " 7 ", "+3", "-1", "0", "1_0", "5.0", "5.5", "1e3", "abc", "٣", "99999999999999999999", "32767", "32768", "0x10"]
FUZZ_DECIMALS = ["", "1", "1.5", " 2.50 ", "-1", "+3.25", "1e2", "1E+2", "0.00001", "123456789012", "12345678901.12", "1.234", ".5", "5.", "abc", "1_000", "inf", "nan", "1,5", "٣.٥"]
FUZZ_DATES = ["", "2026-01-02", "2026-1-2", "01/02/2026", "01/02/26", "2026-02-30", "2026/01/02", "02-01-2026", "Jan 2 2026", " 2026-01-02 ", "0001-01-01", "99999-01-01", "abc"]
FUZZ_TIMES = ["", "10:20", "10:20:30", "10:20:30.123", "25:00", "10", "10:20 PM", "1:2:3", "abc"]
FUZZ_TEXT = ["", " ", "  padded  ", "x" * 255, "x" * 256, "tab\there", "línea\nbreak", "ünï©ode", "<b>&amp;</b>", "'quote\"", "nul\x00", "\u0d2a\u0d3e\u0d32"]
FUZZ_EMAIL = ["", "a@b.co", "a@b", "a@localhost", "a b@c.com", "@x.com", "x@y..com", "x@[1.2.3.4]", "x@bücher.de", "very.long." + "a" * 60 + "@example.com", "ok+tag@example.org", "a@b.c-"]


def fuzz(r, path, base, field, values, brief=True, keep=None):
    for i, v in enumerate(values):
        data = dict(base)
        data[field] = v
        r.post(path, data, f"{field}={v!r}", brief=field if brief else None, follow=False)
        if keep:
            keep(r)


@scenario
def fuzz_flow(r):
    oid = org_fixture(r, "forg", "TwinOrg Fuzz")
    uid = user_fixture(r, "twin_fz")
    tpath = "/admin/ledger/registeredtoken/add/"
    mg = inline_mgmt("receipts")
    tbase = dict({"organization": str(oid), "token": "TFZ", "presenter_name_ml": "", "presentation_date": "2026-01-02", "presenter_place_ml": "",
                  "presenter_address_ml": "", "presenter_city_ml": "", "presented_at_0": "", "presented_at_1": "", "document_amount": "",
                  "applicant_name": "", "sro": "", "status": "pending", "attempts": "0", "last_error": "", "collected_at_0": "", "collected_at_1": ""}, **mg)
    n = [0]

    def post_unique(field, values):
        for v in values:
            n[0] += 1
            data = dict(tbase, token="TFZ%d" % n[0])
            data[field] = v
            r.post(tpath, data, f"{field}={v!r}", brief=field, follow=False)

    post_unique("attempts", FUZZ_INTS)
    post_unique("document_amount", FUZZ_DECIMALS)
    post_unique("presentation_date", FUZZ_DATES)
    post_unique("presented_at_0", FUZZ_DATES)
    for t in FUZZ_TIMES:
        n[0] += 1
        r.post(tpath, dict(tbase, token="TFZ%d" % n[0], presented_at_0="2026-01-02", presented_at_1=t), f"time={t!r}", brief="presented_at", follow=False)
    post_unique("applicant_name", FUZZ_TEXT)
    post_unique("last_error", FUZZ_TEXT)
    post_unique("status", ["pending", "collected", "failed", "zzz", "", "PENDING"])
    # rows the valid ones created
    r.snap("tokens", "SELECT token, presentation_date, presented_at, document_amount, applicant_name, last_error, status, attempts FROM ledger_registeredtoken WHERE token LIKE 'TFZ%%' ORDER BY token")
    find_and_clean(r, "ledger_registeredtoken", "token LIKE 'TFZ%%'", (), "tfz", None)
    r.created.append(("DELETE FROM ledger_registeredtoken WHERE token LIKE 'TFZ%%'", ()))
    upath = "/admin/auth/user/%d/change/" % uid
    ubase = {"username": "twin_fz", "first_name": "", "last_name": "", "email": "", "is_active": "on", "date_joined_0": "2020-01-01", "date_joined_1": "00:00:00",
             "initial-date_joined_0": "2020-01-01", "initial-date_joined_1": "00:00:00", "last_login_0": "", "last_login_1": ""}
    fuzz(r, upath, ubase, "email", FUZZ_EMAIL)
    fuzz(r, upath, ubase, "first_name", FUZZ_TEXT)
    for t in FUZZ_TIMES:
        r.post(upath, dict(ubase, last_login_0="2026-01-02", last_login_1=t), f"last_login time={t!r}", brief="last_login", follow=False)
    for d in FUZZ_DATES:
        r.post(upath, dict(ubase, last_login_0=d, last_login_1="10:00:00"), f"last_login date={d!r}", brief="last_login", follow=False)
    r.snap("user", "SELECT username, first_name, email, last_login FROM auth_user WHERE id=%s", (uid,))
    cpath = "/admin/accounts/portalcredential/add/"
    cbase = {"organization": str(oid), "portal_username": "u", "new_deed_menu_index": "1", "my_deeds_menu_index": "2", "new_password": ""}
    fuzz(r, cpath, cbase, "my_deeds_menu_index", FUZZ_INTS)
    r.created.append(("DELETE FROM accounts_portalcredential WHERE organization_id=%s", (oid,)))
    r.created.append(("DELETE FROM django_admin_log WHERE object_repr LIKE 'Portal credential for TwinOrg Fuzz%%' OR object_repr LIKE 'TFZ%%'", ()))
    r.created.append(("DELETE FROM django_admin_log WHERE object_repr='twin_fz'", ()))


@scenario
def own_password_flow(r):
    pw = "pw-Twin-staff-2"
    uid = user_fixture(r, "twin_staff2", password=pbkdf2(pw), is_staff=True, email="s@example.com")
    staff = Site(r.site.base, "twin_staff2", pw)
    staff.login()
    r.site = staff
    path = "/admin/password_change/"
    new = "N3w-Twin-pass-99"
    r.get(path, body=True)
    r.post(path, {"old_password": "", "new_password1": "", "new_password2": ""}, "empty")
    r.post(path, {"old_password": "wrong", "new_password1": new, "new_password2": new}, "wrong old")
    r.post(path, {"old_password": pw, "new_password1": new, "new_password2": "other"}, "mismatch")
    r.post(path, {"old_password": pw, "new_password1": "short", "new_password2": "short"}, "weak")
    r.post(path, {"old_password": pw, "new_password1": "twin_staff2", "new_password2": "twin_staff2"}, "similar")
    r.post(path, {"old_password": pw, "new_password1": new, "new_password2": new}, "ok")
    r.get("/admin/password_change/done/", "done", body=True)
    r.get("/admin/", "index still logged in", body=False)
    r.snap("hash", "SELECT (password LIKE 'pbkdf2_sha256$%%') AS hashed FROM auth_user WHERE id=%s", (uid,))


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
