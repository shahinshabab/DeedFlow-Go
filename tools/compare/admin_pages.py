"""Compares pages of the Django admin and the Go admin (same database).

    admin_pages.py [--anon] PATH [PATH...]
"""
import argparse
import difflib
import re
import sys
import os

sys.path.insert(0, os.path.dirname(__file__))
from pages import Site, norm  # noqa: E402

UUID = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")


def prep(html):
    html = re.sub(r'name="csrfmiddlewaretoken" value="[^"]*"', 'name="csrfmiddlewaretoken" value="TOKEN"', html)
    return norm(html)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--anon", action="store_true")
    ap.add_argument("--context", type=int, default=2)
    ap.add_argument("paths", nargs="+")
    args = ap.parse_args()
    dj, go = Site("http://127.0.0.1:8091", "admin", "correct-horse-7"), Site("http://127.0.0.1:8090", "admin", "correct-horse-7")
    if not args.anon:
        dj.login()
        go.login()
    else:
        dj.req("GET", "/admin/login/")
        go.req("GET", "/admin/login/")
    bad = 0
    for path in args.paths:
        s1, h1, b1 = dj.req("GET", path)
        s2, h2, b2 = go.req("GET", path)
        if s1 != s2:
            print(f"{path}: status django={s1} go={s2}")
            bad += 1
            continue
        if s1 in (301, 302):
            l1, l2 = h1.get("Location"), h2.get("Location")
            print(f"{path}: redirect {'same' if l1 == l2 else f'DIFFERENT {l1} vs {l2}'}")
            bad += l1 != l2
            continue
        a, b = prep(b1), prep(b2)
        if a == b:
            print(f"{path}: identical ({len(a)} nodes)")
            continue
        bad += 1
        print(f"{path}: DIFFERS")
        for line in list(difflib.unified_diff(a, b, "django", "go", lineterm="", n=args.context))[:80]:
            print("   ", line)
    sys.exit(1 if bad else 0)


main()
