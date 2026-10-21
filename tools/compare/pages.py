"""Fetches the same pages from the running Django site and the Go site (both
on the same database) and diffs their normalised HTML.

    pages.py [--django http://127.0.0.1:8091] [--go http://127.0.0.1:8090] PATH [PATH...]
    (PATH may contain {deed} = the public id of a deed to look at)
"""
import argparse
import difflib
import http.cookiejar
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from html.parser import HTMLParser


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


class Site:
    def __init__(self, base, user, password):
        self.base = base
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar), NoRedirect)
        self.user, self.password = user, password

    def csrf(self):
        for c in self.jar:
            if c.name == "csrftoken":
                return c.value
        return ""

    def req(self, method, path, data=None, headers=None, raw_body=None, content_type=None, binary=False):
        url = self.base + path
        h = dict(headers or {})
        body = raw_body
        if method == "POST" and raw_body is None:
            d = dict(data or {})
            d.setdefault("csrfmiddlewaretoken", self.csrf())
            body = urllib.parse.urlencode(d, doseq=True).encode()
            h["Content-Type"] = "application/x-www-form-urlencoded"
        if content_type:
            h["Content-Type"] = content_type
        if method == "POST":
            h["Referer"] = self.base + "/"
        r = urllib.request.Request(url, data=body, method=method, headers=h)
        try:
            resp = self.opener.open(r)
        except urllib.error.HTTPError as e:
            resp = e
        status = getattr(resp, "status", None) or resp.code
        data = resp.read()
        return status, resp.headers, data if binary else data.decode("utf-8", "replace")

    def login(self):
        self.req("GET", "/login/")
        s, h, _ = self.req("POST", "/login/", {"username": self.user, "password": self.password})
        if s != 302:
            raise SystemExit(f"login to {self.base} failed: {s}")


class Norm(HTMLParser):
    SKIP_ATTRS = {"nonce"}

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.out = []
        self.skip = 0

    def handle_starttag(self, tag, attrs):
        a = []
        for k, v in attrs:
            if v is None:
                v = ""
            if k in self.SKIP_ATTRS:
                continue
            if k in ("href", "src") and "/static/" in v:
                v = re.sub(r"(/static/[^?]*?)(\.[0-9a-f]{12})?(\.[a-z0-9]+)(\?v=.*)?$", r"\1\3", v)
            if tag == "input" and k == "value" and dict(attrs).get("name") == "csrfmiddlewaretoken":
                v = "TOKEN"
            a.append(f'{k}="{v}"')
        a.sort()
        self.out.append("<" + tag + (" " + " ".join(a) if a else "") + ">")
        if tag in ("script", "style"):
            self.skip += 1

    def handle_endtag(self, tag):
        if tag in ("script", "style") and self.skip:
            self.skip -= 1
        self.out.append(f"</{tag}>")

    def handle_data(self, data):
        text = re.sub(r"\s+", " ", data).strip()
        text = re.sub(r"\?v=[0-9a-f]{6,}", "", text)
        if text:
            self.out.append(text)


def norm(html):
    html = re.sub(r"<!--.*?-->", "", html, flags=re.S)
    p = Norm()
    p.feed(html)
    return p.out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--django", default="http://127.0.0.1:8091")
    ap.add_argument("--go", default="http://127.0.0.1:8090")
    ap.add_argument("--user", default="admin")
    ap.add_argument("--password", default="correct-horse-7")
    ap.add_argument("--deed", default="")
    ap.add_argument("--context", type=int, default=2)
    ap.add_argument("paths", nargs="+")
    args = ap.parse_args()
    dj, go = Site(args.django, args.user, args.password), Site(args.go, args.user, args.password)
    dj.login()
    go.login()
    bad = 0
    for path in args.paths:
        path = path.replace("{deed}", args.deed)
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
        if "json" in (h1.get("Content-Type") or ""):
            import json
            same = json.loads(b1) == json.loads(b2)
            print(f"{path}: {s1} {'same json' if same else 'DIFFERENT json'}")
            if not same:
                print("   django:", b1[:300])
                print("   go:    ", b2[:300])
            bad += not same
            continue
        if "html" not in (h1.get("Content-Type") or ""):
            print(f"{path}: {s1} {'same' if b1 == b2 else 'DIFFERENT'} ({h1.get('Content-Type')})")
            bad += b1 != b2
            continue
        n1, n2 = norm(b1), norm(b2)
        if n1 == n2:
            print(f"{path}: identical ({len(n1)} nodes)")
            continue
        bad += 1
        print(f"{path}: DIFFERS")
        diff = list(difflib.unified_diff(n1, n2, "django", "go", n=args.context, lineterm=""))
        for line in diff[:80]:
            print("   ", line)
        if len(diff) > 80:
            print(f"    ... {len(diff) - 80} more lines")
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
