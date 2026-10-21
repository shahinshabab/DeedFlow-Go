"""Writes internal/web/testdata/admin_oracle.json: what Django answers for the
parsing and validation helpers the Go admin re-implements.

    gen_oracle.py > internal/web/testdata/admin_oracle.json
"""
import json
import sys
from decimal import Decimal, InvalidOperation

import django

django.setup()

from django import forms  # noqa: E402
from django.contrib.admin.utils import quote, unquote  # noqa: E402
from django.core.exceptions import ValidationError  # noqa: E402
from django.core.validators import validate_email  # noqa: E402
from django.forms import Media  # noqa: E402
from django.utils.text import smart_split  # noqa: E402

out = {}

# smart_split
out["smart_split"] = []
for s in ['a b', ' a  b ', '"a b" c', "'a b' c", 'a"b c"d e', '"unterminated x', 'x "a \\" b" y', "it's here", '', '   ', 'a\tb\nc']:
    out["smart_split"].append({"in": s, "out": list(smart_split(s))})

# admin quote/unquote
out["quote"] = []
for s in ["abc", "a/b", "a_b", "a:b", "a b", "ünï", "a#b?c;d@e&f=g+h$i,j\"k[l]m<n>o%p\nq\\r", "_5F"]:
    out["quote"].append({"in": s, "out": quote(s), "back": unquote(quote(s))})

# date / time / datetime fields
def run(field, value):
    try:
        v = field.clean(value)
        return {"ok": True, "value": str(v)}
    except ValidationError as e:
        return {"ok": False, "errors": list(e.messages)}

date = forms.DateField(required=False)
time_ = forms.TimeField(required=False)
dates = ["", "2026-01-02", "2026-1-2", "01/02/2026", "01/02/26", "02/30/2026", "2026/01/02", "02-01-2026", "Jan 2 2026", "jan 2, 2026",
         "2 Jan 2026", "2 January, 2026", "January 2 2026", " 2026-01-02 ", "0001-01-01", "0000-01-01", "10000-01-01", "abc", "12/31/68", "12/31/69",
         "Sept 2 2026", "Sep 2 2026", "2026-13-01", "2026-12-32", "2026-02-29", "2024-02-29", "1 1 2026"]
out["date"] = [{"in": d, **run(date, d)} for d in dates]
times = ["", "10:20", "10:20:30", "10:20:30.123", "10:20:30.1234567", "25:00", "10", "10:20 PM", "1:2:3", "23:59:60", "23:59:61", "abc", " 9:05 "]
out["time"] = [{"in": t, **run(time_, t)} for t in times]

# integers and decimals
integer = forms.IntegerField(required=False)
ints = ["", "5", " 7 ", "+3", "-1", "0", "1_0", "5.0", "5.00 ", "5.5", "1e3", "abc", "٣", "99999999999999999999", "0x10", "1__0", "_1", "1_", "-", "+", " "]
out["int"] = [{"in": i, **run(integer, i)} for i in ints]

def dec(max_digits, places):
    return forms.DecimalField(required=False, max_digits=max_digits, decimal_places=places)

decimals = ["", "1", "1.5", " 2.50 ", "-1", "+3.25", "1e2", "1E+2", "0.00001", "123456789012", "12345678901.12", "1.234", ".5", "5.", "abc", "1_000",
            "inf", "nan", "-inf", "1,5", "٣.٥", "0", "-0", "00012.5", "1e-3", "1.50e1"]
out["decimal"] = []
for md, pl in [(10, 4), (12, 2), (None, None), (5, 0), (1, 0)]:
    f = dec(md, pl)
    for d in decimals:
        r = run(f, d)
        if r["ok"] and r["value"] != "None":
            pass
        out["decimal"].append({"max_digits": md, "places": pl, "in": d, **r})

# emails
emails = ["", "a@b.co", "a@b", "a@localhost", "a b@c.com", "@x.com", "x@y..com", "x@[1.2.3.4]", "x@[1.2.3]", "x@bücher.de", "very.long." + "a" * 60 + "@example.com",
          "ok+tag@example.org", "a@b.c-", "a@-b.com", "\"quoted user\"@example.com", "a@b.c", "a@b.com.", "A@B.COM", "x@xn--bcher-kva.de", "日本@example.com", "a@" + "b" * 64 + ".com",
          "x@[IPv6:::1]", "x@[::1]"]
out["email"] = []
for e in emails:
    try:
        validate_email(e)
        out["email"].append({"in": e, "ok": True})
    except ValidationError:
        out["email"].append({"in": e, "ok": False})

# media
def merged(lists):
    m = Media()
    m._js_lists = lists
    return m._js

out["media"] = [
    {"lists": [["a", "b", "c"], ["a", "c"]], "out": merged([["a", "b", "c"], ["a", "c"]])},
    {"lists": [["j", "ji", "core", "rel", "act"], ["cal", "dts"], ["j", "ji", "inl"]], "out": merged([["j", "ji", "core", "rel", "act"], ["cal", "dts"], ["j", "ji", "inl"]])},
    {"lists": [["a", "b"], ["b", "a"]], "out": merged([["a", "b"], ["b", "a"]])},
    {"lists": [["x"], ["y"], ["x", "z"]], "out": merged([["x"], ["y"], ["x", "z"]])},
    {"lists": [["jq", "init", "core", "rel", "actions", "urlify", "prepop", "xreg"], ["jq", "s2", "en", "init", "ac"], ["jq", "init", "inl"], ["core", "sb", "sf"]],
     "out": merged([["jq", "init", "core", "rel", "actions", "urlify", "prepop", "xreg"], ["jq", "s2", "en", "init", "ac"], ["jq", "init", "inl"], ["core", "sb", "sf"]])},
]

# JSON re-dump
out["json_dump"] = []
for s in ['{"a":1,"b":[1,2,3]}', '[1,2.5,1e5,-0.0,true,null,"é\\n"]', '{"z": {"y": []}}', '  [ ]  ', '{bad', '', '1', '"s"', '1.0', '12345678901234567890', '{"a":1}{"b":2}', '[1,]']:
    try:
        v = json.loads(s)
        out["json_dump"].append({"in": s, "out": json.dumps(v, ensure_ascii=False)})
    except Exception:  # noqa: BLE001
        out["json_dump"].append({"in": s, "out": None})

json.dump(out, sys.stdout, indent=1, ensure_ascii=False)
