"""Runs the File reader scenarios (read, convert for InDesign, filing sheets
new / edit / view / save / convert) against the Django and the Go site and
compares the answers and the rows left behind.

    filereader_flow.py FILES_DIR
"""
import io
import json
import os
import re
import sys
import uuid
import zipfile

sys.path.insert(0, os.path.dirname(__file__))
from pages import Site, norm  # noqa: E402

import psycopg2  # noqa: E402

DB = dict(host="127.0.0.1", port=5433, dbname="deedflow", user="deedflow", password="deedflow")
UUID = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")


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


def post_files(site, path, fields, files):
    fields = list(fields) + [("csrfmiddlewaretoken", site.csrf())]
    body, ctype = multipart(fields, files)
    return site.req("POST", path, raw_body=body, content_type=ctype, binary=True)


def page_summary(b):
    html = b.decode("utf-8", "replace")
    scripts = {}
    for m in re.finditer(r'<script id="([^"]+)" type="application/json">(.*?)</script>', html, re.S):
        scripts[m.group(1)] = json.loads(m.group(2))
    html = re.sub(r'<script id="[^"]+" type="application/json">.*?</script>', "", html, flags=re.S)
    html = UUID.sub("<uuid>", html)
    html = re.sub(r'FS_CSRF = "[^"]*"', 'FS_CSRF = "TOKEN"', html)
    html = re.sub(r"\d{2}:\d{2}", "HH:MM", html)
    return norm(html), scripts


def zip_summary(b):
    with zipfile.ZipFile(io.BytesIO(b)) as z:
        return {i.filename: (zip_summary(z.read(i)) if i.filename.endswith(".idml") else z.read(i).decode("utf-8"))
                for i in z.infolist()}


def compare_responses(name, a, b):
    sa, ha, ba = a
    sb, hb, bb = b
    problems = []
    if sa != sb:
        problems.append(f"status {sa} vs {sb}")
    for h in ("Content-Type", "Content-Disposition", "Location"):
        if h == "Content-Type" and sa >= 400 and sb >= 400:
            continue
        va, vb = ha.get(h), hb.get(h)
        if va is not None and vb is not None:
            va, vb = UUID.sub("<uuid>", va), UUID.sub("<uuid>", vb)
        if va != vb:
            problems.append(f"{h}: {va!r} vs {vb!r}")
    ct = ha.get("Content-Type") or ""
    if not problems and sa == 200:
        if "html" in ct:
            na, sa_ = page_summary(ba)
            nb, sb_ = page_summary(bb)
            if na != nb:
                import difflib
                d = [l for l in difflib.unified_diff(na, nb, lineterm="", n=1)][:30]
                problems.append("html differs:\n  " + "\n  ".join(d))
            if sa_ != sb_:
                problems.append("embedded json differs: " + ", ".join(k for k in sa_ if sa_.get(k) != sb_.get(k)))
        elif "json" in ct:
            if json.loads(ba) != json.loads(bb):
                problems.append(f"json differs:\n {ba[:300]!r}\n {bb[:300]!r}")
        elif "zip" in ct or "idml" in ct:
            za, zb = zip_summary(ba), zip_summary(bb)
            if za != zb:
                problems.append("zip differs: " + ", ".join(k for k in set(za) | set(zb) if za.get(k) != zb.get(k)))
        elif ba != bb:
            problems.append("body differs")
    print(f"{name}: {'OK' if not problems else 'DIFFERENT'} ({sa})")
    for p in problems:
        print("   ", p)
    return not problems


def main(files_dir):
    dj, go = Site("http://127.0.0.1:8091", "admin", "correct-horse-7"), Site("http://127.0.0.1:8090", "admin", "correct-horse-7")
    dj.login()
    go.login()
    ok = True

    def both(name, fn):
        nonlocal ok
        ok &= compare_responses(name, fn(dj), fn(go))

    def read(fn):
        return open(os.path.join(files_dir, fn), "rb").read()

    for path in ("/file-reader/", "/file-reader/filing-sheets/", "/file-reader/filing-sheets/new/",
                 "/file-reader/filing-sheet/", "/file-reader/filing-sheets/?q=zzz"):
        both("GET " + path, lambda s, p=path: s.req("GET", p, binary=True))

    ctype = {"pdf": "application/pdf", "idml": "application/octet-stream", "pmd": "application/octet-stream"}
    uploads = ["sample.pmd", "sample.idml", "deed.pdf", "table.pdf", "g0000.idml", "g0007.idml", "fz0000.pmd", "fz0001.pmd"]
    for fn in uploads:
        data = read(fn)
        both("read " + fn, lambda s, fn=fn, data=data: post_files(s, "/file-reader/", [("action", "read")],
                                                                     [("file", fn, data, ctype[fn.rsplit(".", 1)[1]])]))
        if fn.endswith(".pmd"):
            for kind in ("idml", "jsx"):
                both(f"convert {kind} {fn}", lambda s, fn=fn, data=data, kind=kind: post_files(
                    s, "/file-reader/", [("action", kind)], [("file", fn, data, "application/octet-stream")]))
    both("read no file", lambda s: post_files(s, "/file-reader/", [("action", "read")], []))
    both("read txt", lambda s: post_files(s, "/file-reader/", [("action", "read")], [("file", "a.txt", b"hello", "text/plain")]))
    both("read indd", lambda s: post_files(s, "/file-reader/", [("action", "read")], [("file", "a.indd", b"hello", "text/plain")]))
    both("read bad pdf", lambda s: post_files(s, "/file-reader/", [("action", "read")], [("file", "a.pdf", b"%PDF-1.4 junk", "application/pdf")]))
    both("read bad idml", lambda s: post_files(s, "/file-reader/", [("action", "read")], [("file", "a.idml", b"PK\x03\x04junk", "application/zip")]))
    both("convert pdf", lambda s: post_files(s, "/file-reader/", [("action", "idml")], [("file", "deed.pdf", read("deed.pdf"), "application/pdf")]))
    multi = [("file", fn, read(fn), "application/octet-stream") for fn in ("sample.pmd", "fz0000.pmd", "fz0001.pmd")]
    multi_bad = multi + [("file", "x.txt", b"zz", "text/plain"), ("file", "sample.pmd", read("sample.pmd"), "application/octet-stream")]
    for kind in ("idml", "jsx"):
        both(f"convert many {kind}", lambda s, kind=kind: post_files(s, "/file-reader/", [("action", kind)], multi_bad))
        both(f"convert many none {kind}", lambda s, kind=kind: post_files(s, "/file-reader/", [("action", kind)],
                                                                          [("file", "a.txt", b"1", "text/plain"), ("file", "b.txt", b"2", "text/plain")]))

    # filing sheets
    created = {}
    for site, tag in ((dj, "dj"), (go, "go")):
        ids = []
        for fn in ("sample.pmd", "table.pdf", "g0000.idml", "fz0000.pmd"):
            s, h, b = post_files(site, "/file-reader/filing-sheets/new/", [], [("file", fn, read(fn), "application/octet-stream")])
            ids.append((s, h.get("Location")))
        created[tag] = ids
    for a, b in zip(created["dj"], created["go"]):
        same = a[0] == b[0] and UUID.sub("<uuid>", a[1] or "") == UUID.sub("<uuid>", b[1] or "")
        print("filing new:", "OK" if same else f"DIFFERENT {a} {b}")
        ok &= same
    both("filing new no file", lambda s: post_files(s, "/file-reader/filing-sheets/new/", [], []))
    both("filing new txt", lambda s: post_files(s, "/file-reader/filing-sheets/new/", [], [("file", "a.txt", b"zz", "text/plain")]))

    def uuids(site, tag):
        return [UUID.search(l[1]).group(0) for l in created[tag]]

    dj_ids, go_ids = uuids(dj, "dj"), uuids(go, "go")
    for k in range(len(dj_ids)):
        for view in ("edit", "view"):
            a = dj.req("GET", f"/file-reader/filing-sheets/{dj_ids[k]}/{view}/", binary=True)
            b = go.req("GET", f"/file-reader/filing-sheets/{go_ids[k]}/{view}/", binary=True)
            ok &= compare_responses(f"filing {view} #{k}", a, b)
        body = json.dumps({"content": "<p>edited ക</p>", "settings": {"size": 14, "x": [1, 2]}, "pages": 3}).encode()
        resp = []
        for site, sid in ((dj, dj_ids[k]), (go, go_ids[k])):
            resp.append(site.req("POST", f"/file-reader/filing-sheets/{sid}/save/", raw_body=body, binary=True,
                                 content_type="application/json", headers={"X-CSRFToken": site.csrf()}))
        same = resp[0][0] == resp[1][0] and set(json.loads(resp[0][2])) == set(json.loads(resp[1][2])) and \
            re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d{6})?\+00:00", json.loads(resp[1][2])["saved"])
        print(f"filing save #{k}:", "OK" if same else f"DIFFERENT {resp[0][2][:100]} {resp[1][2][:100]}")
        ok &= bool(same)
        a = dj.req("GET", f"/file-reader/filing-sheets/{dj_ids[k]}/edit/", binary=True)
        b = go.req("GET", f"/file-reader/filing-sheets/{go_ids[k]}/edit/", binary=True)
        ok &= compare_responses(f"filing edit after save #{k}", a, b)
    for bad in (b"not json", b"[]", json.dumps({"settings": {}}).encode(), json.dumps({"content": "x", "pages": "z"}).encode(),
                json.dumps({"content": "x", "pages": 20000}).encode(), json.dumps({"content": "x", "settings": [1]}).encode(),
                json.dumps({"content": 5, "settings": None, "pages": None}).encode()):
        resp = [site.req("POST", f"/file-reader/filing-sheets/{sid}/save/", raw_body=bad, binary=True,
                         content_type="application/json", headers={"X-CSRFToken": site.csrf()})
                for site, sid in ((dj, dj_ids[0]), (go, go_ids[0]))]
        same = resp[0][0] == resp[1][0] and (resp[0][0] != 200 or True) and (json.loads(resp[0][2]) == json.loads(resp[1][2]) or resp[0][0] == 200)
        print(f"filing save bad {bad[:30]!r}:", "OK" if same else f"DIFFERENT {resp[0][0]} {resp[1][0]} {resp[0][2][:80]} {resp[1][2][:80]}")
        ok &= bool(same)
    both("filing save GET", lambda s: s.req("GET", f"/file-reader/filing-sheets/{dj_ids[0]}/save/" if s is dj else f"/file-reader/filing-sheets/{go_ids[0]}/save/", binary=True))
    both("filing edit 404", lambda s: s.req("GET", "/file-reader/filing-sheets/00000000-0000-0000-0000-000000000000/edit/", binary=True))

    texts = ["B[mcw", "sImÃw \\mfw", "കൊല്ലം ആധാരം", "", "  ", "Hello world", "ക്ര ക്യ"]
    for to in ("legacy", "unicode"):
        body = json.dumps({"to": to, "texts": texts}).encode()
        both("convert " + to, lambda s, body=body: s.req("POST", "/file-reader/filing-sheet/convert/", raw_body=body, binary=True,
                                                         content_type="application/json", headers={"X-CSRFToken": s.csrf()}))
    for bad in (b"x", json.dumps({"to": "nope", "texts": []}).encode(), json.dumps({"to": "legacy"}).encode(),
                json.dumps({"to": "legacy", "texts": [1, None, True]}).encode()):
        both(f"convert bad {bad[:30]!r}", lambda s, bad=bad: s.req("POST", "/file-reader/filing-sheet/convert/", raw_body=bad, binary=True,
                                                                    content_type="application/json", headers={"X-CSRFToken": s.csrf()}))

    both("list", lambda s: s.req("GET", "/file-reader/filing-sheets/", binary=True))
    both("list search", lambda s: s.req("GET", "/file-reader/filing-sheets/?q=sample", binary=True))

    # stored rows
    with psycopg2.connect(**DB) as c, c.cursor() as cur:
        cur.execute("select name, source_format, sheet::text, content, settings::text, pages, owner_id, organization_id "
                    "from filereader_filingsheet order by id")
        rows = cur.fetchall()
    half = len(rows) // 2
    dj_rows = [json.loads(r[2]) for r in rows if True]
    print("stored rows:", len(rows))
    names = [r[0] for r in rows]
    for k in range(4):
        a = [r for r in rows if r[0] == names[k]]
    cmp_ok = True
    by_name = {}
    for r in rows:
        by_name.setdefault(r[0], []).append(r)
    for name, rs in by_name.items():
        if len(rs) >= 2:
            a, b = rs[-2], rs[-1]
            same = (a[1], json.loads(a[2]), a[3], json.loads(a[4]), a[5], a[6], a[7]) == (b[1], json.loads(b[2]), b[3], json.loads(b[4]), b[5], b[6], b[7])
            print("row", name, "OK" if same else f"DIFFERENT {a[1]}/{b[1]} {a[5]}/{b[5]}")
            cmp_ok &= same
    ok &= cmp_ok
    print("ALL OK" if ok else "SOME DIFFERENT")


if __name__ == "__main__":
    main(sys.argv[1])
