"""Runs the Django project's filereader on generated model documents and on
real files and writes what it produces as JSON, for the Go port's tests to
compare against.

    PYTHONPATH=<DeedFlow>/backend DJANGO_SETTINGS_MODULE=config.settings \
        python oracle.py models  OUT.json COUNT SEED
    ... python oracle.py files   OUT.json FILE...
    ... python oracle.py legacy  OUT.json
"""

import io
import json
import os
import random
import sys
import zipfile

import django

django.setup()

from filereader import filing, idml_writer, jsx_writer, legacy  # noqa: E402
from filereader.readers import read_file  # noqa: E402
from filereader.model import ReaderError  # noqa: E402

FONTS = ["ML-TTKarthika", "Times New Roman", "Arial Baltic", "FML-TTIndulekha", ""]
WORDS = ["Sale", "deed", "No.", "1234/2024", "John", "Doe", "Village", "Kollam", "B[mcw", "sImÃw", "\\mfw",
         "the", "party", "of", "first", "part", "12", "3.5", "Rs.", "5,00,000/-", "¨", "½"]


def rtext(rng, lo=1, hi=12):
    parts = []
    for _ in range(rng.randint(lo, hi)):
        parts.append(rng.choice(WORDS))
        parts.append(rng.choice([" ", " ", " ", "  ", "\t", "\n", "", ""]))
    return "".join(parts)


def span(rng, t=None):
    return {"t": rtext(rng) if t is None else t, "font": rng.choice(FONTS), "size": rng.choice([10, 11, 12, 12, 14.5]),
            "bold": rng.random() < .2, "italic": rng.random() < .1, "underline": rng.random() < .1,
            "color": rng.choice(["#000000", "#ff0000", "#12ab34", "none"]), "legacy": rng.random() < .3}


def para(rng, spans=None):
    return {"kind": "p", "align": rng.choice(["left", "left", "center", "right", "justify"]),
            "indent": rng.choice([0, 0, 0, 18, 1.5]), "left": rng.choice([0, 0, 10]), "right": 0,
            "before": rng.choice([0, 6]), "after": rng.choice([0, 0, 4]),
            "leading": rng.choice([None, None, 14.4, 18]),
            "spans": spans if spans is not None else [span(rng) for _ in range(rng.randint(0, 3))]}


def story(rng):
    blocks = [para(rng) for _ in range(rng.randint(0, 3))]
    if rng.random() < .1:
        blocks.append({"kind": "table", "cols": [60, 120], "rows": [
            {"cells": [{"col": 0, "blocks": [para(rng)], "colspan": 1, "rowspan": 1, "rotate": rng.choice([0, 0, 90])},
                       {"col": 1, "blocks": [para(rng)], "colspan": 1, "rowspan": 1, "rotate": 0}]},
            {"cells": [{"col": 0, "blocks": [para(rng)], "colspan": 2, "rowspan": 1, "rotate": 0}]}]})
    return blocks


def rect_d(x0, y0, x1, y1):
    return "M%.2f %.2f L%.2f %.2f L%.2f %.2f L%.2f %.2f Z" % (x0, y0, x1, y0, x1, y1, x0, y1)


def gen_doc(rng):
    doc = {"format": rng.choice(["PageMaker", "PDF", "InDesign (IDML)"]), "name": "t%d.pmd" % rng.randint(1, 99),
           "pages": [], "stories": {}, "warnings": []}
    nstory = [0]

    def new_story(blocks=None):
        nstory[0] += 1
        sid = "s%d" % nstory[0]
        doc["stories"][sid] = story(rng) if blocks is None else blocks
        return sid

    shared = new_story()
    for pg in range(rng.randint(1, 3)):
        W, H = rng.choice([(595.28, 841.89), (612, 792)])
        items = []
        # text frames
        for _ in range(rng.randint(0, 5)):
            sid = shared if rng.random() < .25 else new_story()
            items.append({"type": "text", "x": round(rng.uniform(20, 400), 2), "y": round(rng.uniform(20, 700), 2),
                          "w": round(rng.uniform(30, 300), 2), "h": rng.choice([0, round(rng.uniform(10, 200), 2)]),
                          "rotate": rng.choice([0, 0, 0, 90, -90, 15.5]), "origin": rng.choice(["center", "top left"]),
                          "story": sid, "inset": [0, 0, 0, 0], "valign": "top", "nowrap": rng.random() < .1,
                          **({"repeat": True} if rng.random() < .1 else {})})
        # a ruled table with text in its cells
        if rng.random() < .8:
            xs = sorted({round(rng.uniform(40, 560), 1) for _ in range(rng.randint(2, 5))})
            ys = sorted({round(rng.uniform(60, 700), 1) for _ in range(rng.randint(2, 5))})
            if len(xs) >= 2 and len(ys) >= 2 and xs[-1] - xs[0] > 40 and ys[-1] - ys[0] > 40:
                items.append({"type": "path", "d": rect_d(xs[0], ys[0], xs[-1], ys[-1]), "fill": "none",
                              "stroke": "#000000", "sw": rng.choice([1, 1.5, 0.75])})
                for x in xs[1:-1]:
                    if rng.random() < .85:
                        y0, y1 = (ys[0], ys[-1]) if rng.random() < .8 else (ys[0], ys[len(ys) // 2])
                        items.append({"type": "path", "d": "M%.2f %.2f L%.2f %.2f" % (x, y0, x, y1), "fill": "none",
                                      "stroke": "#000000", "sw": rng.choice([0.5, 0.75, 0])})
                for y in ys[1:-1]:
                    if rng.random() < .85:
                        items.append({"type": "path", "d": "M%.2f %.2f L%.2f %.2f" % (xs[0], y, xs[-1], y),
                                      "fill": "none", "stroke": "#000000", "sw": 0.5})
                for r in range(len(ys) - 1):
                    for c in range(len(xs) - 1):
                        if rng.random() < .8:
                            sid = new_story()
                            items.append({"type": "text", "x": round(xs[c] + rng.uniform(0, 5), 2),
                                          "y": round(ys[r] + rng.uniform(0, 5), 2),
                                          "w": round(xs[c + 1] - xs[c], 2), "h": round(ys[r + 1] - ys[r], 2),
                                          "rotate": rng.choice([0, 0, 0, 0, 90, 270]), "origin": "center",
                                          "story": sid, "inset": [0, 0, 0, 0], "nowrap": False})
        if rng.random() < .5:
            items.append({"type": "path", "d": "M10.00 20.00 L300.00 22.00", "fill": "none", "stroke": "#112233",
                          "sw": 1})
        if rng.random() < .3:
            items.append({"type": "path", "d": "M10 10 L50 10 L50 40 Z", "fill": "#ff0000", "stroke": "none", "sw": 0.25})
        if rng.random() < .2:
            items.append({"type": "image", "x": 10, "y": 10, "w": 100, "h": 50, "rotate": 0, "origin": "center",
                          "label": "Image"})
        rng.shuffle(items)
        doc["pages"].append({"width": W, "height": H, "items": items})
    return doc


def zip_entries(data):
    out = {}
    with zipfile.ZipFile(io.BytesIO(data)) as z:
        for info in z.infolist():
            out[info.filename] = z.read(info).decode("utf-8")
    return out


def outputs(doc):
    res = {}
    try:
        res["filing"] = filing.build(json.loads(json.dumps(doc)))
    except Exception as exc:  # pragma: no cover
        res["filing_error"] = repr(exc)
    try:
        res["idml"] = zip_entries(idml_writer.write(json.loads(json.dumps(doc))))
    except Exception as exc:
        res["idml_error"] = repr(exc)
    try:
        res["jsx"] = jsx_writer.write(json.loads(json.dumps(doc)), "My \"deed\" 1").decode("ascii")
    except Exception as exc:
        res["jsx_error"] = repr(exc)
    return res


def models(out, count, seed):
    cases = []
    for i in range(count):
        rng = random.Random(seed * 1000003 + i)
        doc = gen_doc(rng)
        cases.append({"doc": doc, **outputs(doc)})
    json.dump(cases, open(out, "w"))


def files(out, paths):
    from filereader import pagemaker

    cases = []
    for path in paths:
        name = os.path.basename(path)
        case = {"file": name}
        try:
            doc = read_file(path, name)
            case["doc"] = doc
            case.update(outputs(doc))
            if name.lower().endswith((".pmd", ".p65")):
                raw = pagemaker.read(path, convert=False)
                case["raw_doc"] = raw
                case["raw_idml"] = zip_entries(idml_writer.write(json.loads(json.dumps(raw))))
                case["raw_jsx"] = jsx_writer.write(json.loads(json.dumps(raw)), "My \"deed\" 1").decode("ascii")
        except ReaderError as exc:
            case["error"] = str(exc)
        except Exception as exc:
            case["crash"] = repr(exc)
        cases.append(case)
    json.dump(cases, open(out, "w"))


def legacy_cases(out):
    strings = ["B[mcw", "sImÃw", "\\mfw", "കൊല്ലം", "ആധാരം 12/2024 Kollam", "Sale deed No. 1234/2024", "അവൻ വന്നു",
               "ഗ്ഗ ണ്ട റ്റ ച്ച ങ്ക മ്പ മ്മ ന്ന ന്റ", "കെ കേ കൈ കൊ കോ കൌ", "ക്ര ക്യ ക്വ ക്ക്ക", "ഈ ഊ ഐ ഓ ഔ ഒ ർ ൽ ൾ ൻ ൺ",
               "പ്രകാരം ഉള്ള സ്ഥലം", "x ‌ y ‍ z", "mixed മലയാളം and English 123 - dash", "", "   ", "ൻറ"]
    cases = {"to_unicode": [(s, legacy.to_unicode(s)) for s in ["B[mcw", "sImÃw", "\\mfw", "കൊല്ലം", "", "Hello", "Sn¶p", "tIm"]],
             "from_unicode": [(s, [list(p) for p in legacy.from_unicode(s)]) for s in strings],
             "is_legacy": [[n, t, legacy.is_legacy_font(n, t)] for n, t in [
                 ("ML-TTKarthika", ""), ("FML-TTIndulekha", ""), ("ML-Nila", ""), ("MLB-TTAmbili", ""),
                 ("Times New Roman", "x"), ("Arial", ""), ("", ""), ("CIDFont+F2", "sImÃw"), ("CIDFont+F1", "Survey No. 12"),
                 ("Foo", "a\\b"), ("ABCDEF+Karthika", "é"), (" ml-tt ", ""), ("Minion Pro", "[x]")]]}
    json.dump(cases, open(out, "w"))


if __name__ == "__main__":
    mode, out = sys.argv[1], sys.argv[2]
    if mode == "models":
        models(out, int(sys.argv[3]), int(sys.argv[4]))
    elif mode == "files":
        files(out, sys.argv[3:])
    elif mode == "legacy":
        legacy_cases(out)
