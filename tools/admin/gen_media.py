"""Writes the <script>/<link> tags (the form media) each model's admin add and
change pages carry, read from a running Django admin:

    gen_media.py http://127.0.0.1:8091 admin password > internal/web/admin_media.json
"""
import json
import re
import sys
import os

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "compare"))
from pages import Site  # noqa: E402

base, user, password = sys.argv[1:4]
site = Site(base, user, password)
site.login()
models = [
    "auth/group", "auth/user", "accounts/portalcredential", "accounts/organization", "deeds/deed",
    "ledger/registeredtoken", "ledger/receiptsyncrun", "ledger/expense",
]


def media(html):
    i = html.index('<script src="/admin/jsi18n/"></script>') + len('<script src="/admin/jsi18n/"></script>')
    j = html.index("xregexp.js\"></script>", i) + len("xregexp.js\"></script>")
    return html[i:j].strip("\n")


out = {}
for m in models:
    label = m.replace("/", ".")
    _, _, html = site.req("GET", f"/admin/{m}/add/")
    out[label + "|add"] = media(html)
    _, _, lst = site.req("GET", f"/admin/{m}/")
    ids = re.findall(r'name="_selected_action" value="([^"]+)"', lst)
    if ids:
        _, _, html = site.req("GET", f"/admin/{m}/{ids[0]}/change/")
        out[label + "|change"] = media(html)
json.dump(out, sys.stdout, indent=1)
