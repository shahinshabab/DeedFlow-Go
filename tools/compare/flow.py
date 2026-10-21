"""Runs the same wizard scenarios against the Django and the Go site (each on
its own copy of a deed, same database) and compares the pages they answer
with and the rows they leave behind.

    flow.py [scenario ...]
"""
import argparse
import difflib
import json
import os
import re
import sys
from html.parser import HTMLParser

sys.path.insert(0, os.path.dirname(__file__))
from pages import Site, norm  # noqa: E402

import psycopg2  # noqa: E402


class FormParser(HTMLParser):
    """Collects the fields of the (first) <form method=post> that has a wizard."""

    def __init__(self):
        super().__init__()
        self.fields = []  # (name, value) in order
        self.in_form = False
        self.select = None
        self.options = []
        self.textarea = None
        self.buf = ""
        self.forms = 0

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if tag == "form" and (a.get("method") or "").lower() == "post":
            self.in_form = True
            self.forms += 1
        if not self.in_form:
            return
        if tag == "input":
            t = (a.get("type") or "text").lower()
            name = a.get("name")
            if not name or "disabled" in a:
                return
            if t in ("checkbox", "radio"):
                if "checked" in a:
                    self.fields.append((name, a.get("value", "on")))
            elif t in ("submit", "button", "file", "image"):
                return
            else:
                self.fields.append((name, a.get("value", "")))
        elif tag == "select":
            self.select = a.get("name") if "disabled" not in a else None
            self.options = []
        elif tag == "option" and self.select is not None:
            self.options.append((a.get("value", ""), "selected" in a))
        elif tag == "textarea":
            self.textarea = a.get("name") if "disabled" not in a else None
            self.buf = ""

    def handle_endtag(self, tag):
        if tag == "form":
            self.in_form = False
        elif tag == "select" and self.select is not None:
            chosen = [v for v, sel in self.options if sel]
            value = chosen[-1] if chosen else (self.options[0][0] if self.options else "")
            self.fields.append((self.select, value))
            self.select = None
        elif tag == "textarea" and self.textarea is not None:
            self.fields.append((self.textarea, self.buf.lstrip("\n")))
            self.textarea = None

    def handle_data(self, data):
        if self.textarea is not None:
            self.buf += data


def form_values(html):
    p = FormParser()
    p.feed(html)
    out = {}
    for k, v in p.fields:
        out.setdefault(k, []).append(v)
    return {k: v[-1] if len(v) == 1 else v for k, v in out.items()}


DB = dict(host="127.0.0.1", port=5433, dbname="deedflow", user="deedflow", password="deedflow")
TABLES = [
    ("deeds_registrationdetail", "deed_id", "id"),
    ("deeds_presenter", "deed_id", "id"),
    ("deeds_priordocument", "deed_id", "position, id"),
    ("deeds_executant", "deed_id", "position, id"),
    ("deeds_claimant", "deed_id", "position, id"),
    ("deeds_scheduleitem", "deed_id", "position, id"),
    ("deeds_witness", "deed_id", "position, id"),
]


def snapshot(public_id):
    conn = psycopg2.connect(**DB)
    try:
        cur = conn.cursor()
        cur.execute("select id, title, status, progress, token_number, result_message, is_test, portal_snapshot::text, survey_mapping::text, extracted_text from deeds_deed where public_id=%s", (public_id,))
        row = cur.fetchone()
        deed_id = row[0]
        out = {"deed": list(row[1:])}
        for table, fk, order in TABLES:
            cur.execute(f"select * from {table} where {fk}=%s order by {order}", (deed_id,))
            cols = [d[0] for d in cur.description]
            rows = []
            for r in cur.fetchall():
                rows.append({c: (str(v) if v is not None else None) for c, v in zip(cols, r) if c not in ("id", "deed_id")})
            out[table] = rows
        return out
    finally:
        conn.close()


def mask(text, deed, num=None):
    text = text.replace(deed, "{deed}")
    if num:
        text = re.sub(r"#%s\b" % num, "#{id}", text)
    return text


class Runner:
    def __init__(self, site, label):
        self.site, self.label = site, label
        self.deed = None
        self.log = []

    def new_deed(self, kind):
        if kind == "blank":
            s, h, _ = self.site.req("GET", "/deeds/new/")
        else:
            s, h, _ = self.site.req("GET", "/deeds/test/selenium/")
        m = re.search(r"/deeds/([0-9a-f-]{36})/", h.get("Location", ""))
        self.deed = m.group(1)
        conn = psycopg2.connect(**DB)
        cur = conn.cursor()
        cur.execute("select id from deeds_deed where public_id=%s", (self.deed,))
        self.num = str(cur.fetchone()[0])
        conn.close()
        return self.deed

    def get(self, path):
        return self.site.req("GET", path.replace("{deed}", self.deed))

    def post(self, path, data):
        return self.site.req("POST", path.replace("{deed}", self.deed), data)


def run_step(runners, step):
    kind = step["do"]
    results = []
    for r in runners:
        path = step.get("path", "/deeds/{deed}/wizard/")
        if kind == "get":
            s, h, b = r.get(path)
        elif kind == "form":  # post the form currently on `from` page, with overrides
            _, _, page = r.get(step.get("from", path))
            data = form_values(page)
            for k, v in step.get("set", {}).items():
                data[k.replace("{deed}", r.deed)] = v
            for k in step.get("drop", []):
                data.pop(k, None)
            for k, v in step.get("extra", {}).items():
                data[k] = v
            s, h, b = r.post(path, data)
        elif kind == "post":
            data = {k: (v.replace("{deed}", r.deed) if isinstance(v, str) else v) for k, v in step["data"].items()}
            s, h, b = r.post(path, data)
        elif kind == "form_current":  # post the form as shown in the previous response
            page = r.last_body
            data = form_values(page)
            for k, v in step.get("set", {}).items():
                data[k] = v
            for k, v in step.get("extra", {}).items():
                data[k] = v
            s, h, b = r.post(path, data)
        else:
            raise SystemExit(f"unknown step {kind}")
        r.last_body = b
        results.append((s, h, b))
    return results


def compare(name, idx, step, runners, results):
    (s1, h1, b1), (s2, h2, b2) = results
    d1, d2 = runners[0].deed, runners[1].deed
    ok = True
    if s1 != s2:
        print(f"  [{name}#{idx}] status django={s1} go={s2}")
        ok = False
    l1, l2 = mask(h1.get("Location") or "", d1), mask(h2.get("Location") or "", d2)
    if l1 != l2:
        print(f"  [{name}#{idx}] Location django={l1} go={l2}")
        ok = False
    if "html" in (h1.get("Content-Type") or "") and s1 == s2 and s1 < 400:
        n1 = [mask(x, d1, runners[0].num) for x in norm(b1)]
        n2 = [mask(x, d2, runners[1].num) for x in norm(b2)]
        # hidden ids differ between the two deeds' rows
        n1 = [re.sub(r'(id|deed)="[^"]*"', '', x) if 'type="hidden"' in x and ('-id"' in x or '-deed"' in x) else x for x in n1]
        n2 = [re.sub(r'(id|deed)="[^"]*"', '', x) if 'type="hidden"' in x and ('-id"' in x or '-deed"' in x) else x for x in n2]
        n1 = [re.sub(r'value="\d+"', 'value="N"', x) if ('-id"' in x or '-deed"' in x) and 'type="hidden"' in x else x for x in n1]
        n2 = [re.sub(r'value="\d+"', 'value="N"', x) if ('-id"' in x or '-deed"' in x) and 'type="hidden"' in x else x for x in n2]
        if n1 != n2:
            ok = False
            print(f"  [{name}#{idx}] page differs")
            for line in list(difflib.unified_diff(n1, n2, "django", "go", n=1, lineterm=""))[:40]:
                print("     ", line)
    st1, st2 = snapshot(d1), snapshot(d2)
    if st1 != st2:
        ok = False
        print(f"  [{name}#{idx}] database state differs")
        for k in st1:
            if st1[k] != st2[k]:
                print("     ", k)
                print("        django:", json.dumps(st1[k], ensure_ascii=False)[:600])
                print("        go:    ", json.dumps(st2[k], ensure_ascii=False)[:600])
    return ok


SCENARIOS = {}


def scenario(name):
    def deco(fn):
        SCENARIOS[name] = fn
        return fn
    return deco


W = "/deeds/{deed}/wizard/"
P = "deed_wizard_{deed}"


@scenario("blank-walk")
def blank_walk():
    """A blank draft, walked through every step."""
    return "blank", [
        {"do": "get"},
        {"do": "form", "path": W, "set": {
            "registration-district": "പാലക്കാട്", "registration-book": "1"}},
    ]


@scenario("edit-presenter")
def edit_presenter():
    return "dummy", [
        {"do": "get", "path": W + "?step=presenter"},
        {"do": "form", "path": W, "from": W + "?step=presenter", "extra": {"save_and_close": "1"},
         "set": {"presenter-name": "Edited Name", "presenter-age": "61"}},
        {"do": "get", "path": "/deeds/{deed}/"},
    ]


@scenario("edit-presenter-invalid")
def edit_presenter_invalid():
    return "dummy", [
        {"do": "form", "path": W, "from": W + "?step=presenter", "extra": {"save_and_close": "1"},
         "set": {"presenter-name": "", "presenter-age": "99999", "presenter-presenter_date": "2020-01-01",
                 "presenter-document_value": "5"}},
    ]


@scenario("edit-executants")
def edit_executants():
    return "dummy", [
        {"do": "form", "path": W, "from": W + "?step=executants", "extra": {"save_and_close": "1"},
         "set": {"executants-0-name": "Changed Executant", "executants-1-name": "New Executant",
                 "executants-1-capacity": "Executant", "executants-TOTAL_FORMS": "2",
                 "executants-ROWS": "1,0"}},
        {"do": "get", "path": W + "?step=executants"},
    ]


@scenario("edit-delete-claimant")
def edit_delete_claimant():
    return "dummy", [
        {"do": "form", "path": W, "from": W + "?step=claimants", "extra": {"save_and_close": "1"},
         "set": {"claimants-0-DELETE": "on"}},
    ]


@scenario("edit-schedule-sum")
def edit_schedule_sum():
    return "dummy", [
        {"do": "form", "path": W, "from": W + "?step=schedule", "extra": {"save_and_close": "1"},
         "set": {"schedule-0-price": "1000"}},
    ]


import datetime as _dt
TODAY = _dt.date.today().isoformat()


@scenario("full-walk")
def full_walk():
    """A blank draft walked through all seven steps and finished."""
    S = "/deeds/{deed}/wizard/"
    return "blank", [
        {"do": "get"},
        {"do": "form_current", "path": S, "set": {
            "registration-district": "പാലക്കാട്", "registration-sro": "വിളയൂർ", "registration-taluk": "പട്ടാമ്പി",
            "registration-book": "1", "registration-deed_type": "വിലയാധാരം",
            "registration-deed_sub_type": "വിലയാധാരം - (തീറാധാരം)--0702"}},
        {"do": "form_current", "path": S, "set": {
            "presenter-presenter_type": "Executant", "presenter-name": "Anil Kumar", "presenter-gender": "Male",
            "presenter-age": "40", "presenter-presenter_date": TODAY, "presenter-stamp_date": TODAY,
            "presenter-document_sheet_count": "5", "presenter-document_value": "125000", "presenter-document_fees": "130000",
            "presenter-stamp_mode": "Physical Paper"}},
        {"do": "form_current", "path": S, "set": {
            "documents-0-document_type": "രജിസ്റ്റർ/ഫയൽ ചെയ്ത", "documents-0-district": "പാലക്കാട്",
            "documents-0-sro": "വിളയൂർ", "documents-0-number": "870", "documents-0-year": "1990",
            "documents-0-book": "1", "documents-0-volume": "275", "documents-0-ROWS": "0",
            "documents-ROWS": "0"}},
        {"do": "form_current", "path": S, "set": {
            "executants-0-capacity": "Executant", "executants-0-name": "Anil Kumar", "executants-0-gender": "Male",
            "executants-0-age": "40", "executants-0-district": "പാലക്കാട്", "executants-0-taluk": "പട്ടാമ്പി",
            "executants-0-village": "കൊപ്പം", "executants-ROWS": "0,1"}},
        {"do": "form_current", "path": S, "set": {
            "claimants-0-capacity": "Claimant", "claimants-0-name": "Latha Devi", "claimants-0-gender": "Female",
            "claimants-0-age": "35", "claimants-ROWS": "0,1"}},
        {"do": "form_current", "path": S, "set": {
            "schedule-0-number": "1", "schedule-0-district": "പാലക്കാട്", "schedule-0-sro": "വിളയൂർ",
            "schedule-0-taluk": "പട്ടാമ്പി", "schedule-0-village": "കൊപ്പം", "schedule-0-resurvey_number": "12",
            "schedule-0-old_survey": "45", "schedule-0-total_are": "10.5", "schedule-0-transfer_are": "10.5",
            "schedule-0-price": "125000", "schedule-0-fair_value": "10000", "schedule-0-details": "details",
            "schedule-ROWS": "0,1"}},
        {"do": "form_current", "path": S, "set": {
            "witnesses-0-witness_type": "attesting", "witnesses-0-name": "Witness One", "witnesses-0-gender": "Male",
            "witnesses-ROWS": "0,1"}},
        {"do": "get", "path": "/deeds/{deed}/"},
    ]


@scenario("walk-errors")
def walk_errors():
    S = "/deeds/{deed}/wizard/"
    return "blank", [
        {"do": "get"},
        {"do": "form_current", "path": S, "set": {"registration-district": "Nowhere"}},
        {"do": "form_current", "path": S, "set": {
            "registration-district": "പാലക്കാട്", "registration-sro": "വിളയൂർ", "registration-taluk": "പട്ടാമ്പി",
            "registration-book": "1", "registration-deed_type": "വിലയാധാരം",
            "registration-deed_sub_type": "വിലയാധാരം - (തീറാധാരം)--0702"}},
        {"do": "form_current", "path": S, "set": {"presenter-name": ""}},
        {"do": "post", "path": S, "data": {"wizard_goto_step": "registration"}},
    ]


@scenario("next-and-back")
def next_and_back():
    return "dummy", [
        {"do": "get", "path": W + "?step=documents"},
        {"do": "form", "path": W, "from": W + "?step=documents"},
        {"do": "post", "path": W, "data": {"wizard_goto_step": "documents", P + "-current_step": "executants"}},
        {"do": "post", "path": W, "data": {P + "-current_step": "bogus"}},
    ]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--django", default="http://127.0.0.1:8091")
    ap.add_argument("--go", default="http://127.0.0.1:8090")
    ap.add_argument("scenarios", nargs="*")
    args = ap.parse_args()
    names = args.scenarios or list(SCENARIOS)
    bad = 0
    for name in names:
        kind, steps = SCENARIOS[name]()
        dj, go = Site(args.django, "admin", "correct-horse-7"), Site(args.go, "admin", "correct-horse-7")
        dj.login()
        go.login()
        runners = [Runner(dj, "django"), Runner(go, "go")]
        for r in runners:
            r.new_deed(kind)
            if kind == "dummy":
                conn = psycopg2.connect(**DB)
                conn.autocommit = True
                conn.cursor().execute("update deeds_deed set status='ready_for_review' where public_id=%s", (r.deed,))
                conn.close()
        print(f"{name}:")
        ok = compare(name, 0, {}, runners, [(200, type("H", (), {"get": lambda s, k, d=None: d})(), "")] * 2) if False else True
        for i, step in enumerate(steps):
            results = run_step(runners, step)
            s0, h0, b0 = results[1]
            m = re.search(r'class="eyebrow">(.*?)</div>', b0, re.S)
            errs = len(re.findall(r'class="field-error"', b0))
            print(f"  step {i}: {step['do']} -> {s0} {h0.get('Location') or ''} {re.sub(r'\s+', ' ', m.group(1)).strip() if m else ''} errors={errs}".replace(runners[1].deed, "{deed}"))
            ok = compare(name, i, step, runners, results) and ok
        print("  OK" if ok else "  FAILED")
        bad += not ok
        for r in runners:
            conn = psycopg2.connect(**DB)
            conn.autocommit = True
            cur = conn.cursor()
            for t in ("deeds_registrationdetail", "deeds_presenter", "deeds_priordocument", "deeds_executant",
                      "deeds_claimant", "deeds_scheduleitem", "deeds_witness", "deeds_deedscreenshot"):
                cur.execute(f"delete from {t} where deed_id=%s", (int(r.num),))
            cur.execute("delete from deeds_deed where public_id=%s", (r.deed,))
            conn.close()
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
