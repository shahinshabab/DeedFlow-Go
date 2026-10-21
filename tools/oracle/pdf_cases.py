"""Builds sample PDFs with reportlab and records what pdfplumber extracts,
for internal/plumber's golden test.  Usage: pdf_cases.py OUTDIR"""
import glob
import json
import os
import sys

import pdfplumber
from reportlab.lib.pagesizes import A4, letter
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from reportlab.pdfgen import canvas

out = sys.argv[1]
os.makedirs(out, exist_ok=True)


def ttf():
    for p in glob.glob("/usr/share/fonts/**/*.ttf", recursive=True):
        if "DejaVuSans.ttf" in p or "LiberationSans-Regular" in p:
            return p
    return None


def text_page():
    c = canvas.Canvas(f"{out}/text.pdf", pagesize=A4)
    c.setFont("Helvetica", 11)
    y = 800
    for line in ["Sale deed executed at Kochi on 12-03-2024", "between Mr. Anil Kumar and Mrs. Latha Devi",
                 "Survey No. 123/4A, Village: Edappally, Taluk: Kanayannur", "Rs. 1,25,000/- (Rupees one lakh twenty five thousand only)"]:
        c.drawString(72, y, line)
        y -= 16
    c.setFont("Times-Roman", 9)
    c.drawString(72, y - 10, "Small print in Times  with   wide   gaps")
    c.setFont("Courier", 10)
    c.drawString(300, y - 10, "mono 0123")
    t = c.beginText(72, 600)
    t.setFont("Helvetica-Bold", 12)
    t.setCharSpace(1.5)
    t.setWordSpace(6)
    t.textLine("Spaced text here")
    t.setCharSpace(0)
    t.setWordSpace(0)
    t.setRise(4)
    t.textLine("raised")
    t.setRise(0)
    t.setLeading(20)
    t.textLines("alpha beta\ngamma delta\nepsilon")
    c.drawText(t)
    c.saveState()
    c.translate(400, 300)
    c.rotate(30)
    c.setFont("Helvetica", 10)
    c.drawString(0, 0, "tilted text")
    c.restoreState()
    c.saveState()
    c.translate(500, 200)
    c.rotate(90)
    c.drawString(0, 0, "sideways words")
    c.restoreState()
    c.setFont("Helvetica", 11)
    c.drawString(72, 100, "Punct, (parens) [x] \"quoted\" 'single' - dash – en • bullet")
    c.showPage()
    c.setPageSize(letter)
    c.setFont("Helvetica", 12)
    c.drawString(50, 700, "Second page")
    c.showPage()
    c.save()


def table_page():
    c = canvas.Canvas(f"{out}/table.pdf", pagesize=A4)
    c.setFont("Helvetica", 9)
    x = [50, 120, 230, 330, 450]
    y = [780, 750, 720, 690, 650]
    for yy in y:
        c.line(x[0], yy, x[-1], yy)
    for xx in x:
        c.line(xx, y[0], xx, y[-1])
    rows = [["Sl", "Survey No", "Extent", "Remarks"], ["1", "12/3", "10.5 Ares", "Dry land"],
            ["2", "45", "2 Cents", "Garden\nwith house"], ["3", "7/1A", "", "N/A"]]
    for r, row in enumerate(rows):
        for ci, txt in enumerate(row):
            for k, part in enumerate(txt.split("\n")):
                c.drawString(x[ci] + 4, y[r] - 14 - 10 * k, part)
    # a second table drawn with rectangles
    c.rect(50, 400, 100, 30)
    c.rect(150, 400, 100, 30)
    c.rect(50, 370, 100, 30)
    c.rect(150, 370, 100, 30)
    c.drawString(55, 412, "A1")
    c.drawString(155, 412, "B1")
    c.drawString(55, 382, "A2")
    c.drawString(155, 382, "B2")
    # a table with a merged cell (ragged)
    xs = [300, 380, 460]
    c.line(300, 430, 460, 430)
    c.line(300, 400, 460, 400)
    c.line(300, 370, 460, 370)
    c.line(300, 430, 300, 370)
    c.line(460, 430, 460, 370)
    c.line(380, 400, 380, 370)
    c.drawString(305, 412, "merged header cell")
    c.drawString(305, 382, "left")
    c.drawString(385, 382, "right")
    # filled rect + curves
    c.setFillColorRGB(0.9, 0.9, 0.9)
    c.rect(50, 200, 200, 40, stroke=0, fill=1)
    c.circle(350, 220, 20)
    c.showPage()
    c.save()


def ttf_page():
    p = ttf()
    if not p:
        return
    pdfmetrics.registerFont(TTFont("TestTTF", p))
    c = canvas.Canvas(f"{out}/ttf.pdf", pagesize=A4)
    c.setFont("TestTTF", 12)
    c.drawString(72, 780, "Embedded TrueType: Kochi éè ₹ 1,000 — ok")
    c.drawString(72, 760, "Second line fi fl")
    c.save()


def rotated_page():
    c = canvas.Canvas(f"{out}/rot.pdf", pagesize=A4)
    c.setPageRotation(90)
    c.setFont("Helvetica", 12)
    c.drawString(100, 700, "Rotated page text")
    c.line(50, 600, 300, 600)
    c.showPage()
    c.save()


def mediabox_page():
    c = canvas.Canvas(f"{out}/mb.pdf", pagesize=A4)
    c.setFont("Helvetica", 12)
    c.drawString(100, 700, "Offset media box")
    c.line(100, 650, 300, 650)
    c.line(100, 650, 100, 600)
    c.showPage()
    c.save()
    # rewrite MediaBox with a non-zero origin
    data = open(f"{out}/mb.pdf", "rb").read()
    data = data.replace(b"/MediaBox [ 0 0 595.2756 841.8898 ]", b"/MediaBox [ 20 30 615.2756 871.8898 ]")
    open(f"{out}/mb.pdf", "wb").write(data)


text_page(); table_page(); ttf_page(); rotated_page(); mediabox_page()

res = {}
for f in sorted(glob.glob(f"{out}/*.pdf")):
    name = os.path.basename(f)
    pages = []
    with pdfplumber.open(f) as pdf:
        for page in pdf.pages:
            def rd(o, keys):
                return {k: o[k] for k in keys}
            chars = [dict(text=c["text"], fontname=c["fontname"], x0=c["x0"], x1=c["x1"], top=c["top"],
                          bottom=c["bottom"], size=c["size"], upright=bool(c["upright"]), adv=c["adv"])
                     for c in page.chars]
            lines = [rd(l, ["x0", "x1", "top", "bottom", "width", "height", "linewidth"]) for l in page.lines]
            rects = [rd(l, ["x0", "x1", "top", "bottom", "width", "height", "linewidth"]) for l in page.rects]
            curves = [dict(rd(l, ["x0", "x1", "top", "bottom", "width", "height"]), pts=[list(p) for p in l["pts"]])
                      for l in page.curves]
            lines_t = page.extract_text_lines(strip=True)
            pages.append(dict(
                width=float(page.width), height=float(page.height), chars=chars, lines=lines, rects=rects, curves=curves,
                text=page.extract_text(),
                text_lines=[dict(text=l["text"], x0=l["x0"], top=l["top"], x1=l["x1"], bottom=l["bottom"], nchars=len(l["chars"])) for l in lines_t],
                text_lines_nostrip=[l["text"] for l in page.extract_text_lines(strip=False)],
                tables=page.extract_tables(),
            ))
    res[name] = pages
json.dump(res, open(f"{out}/expected.json", "w"), ensure_ascii=False)
print("ok", {k: len(v) for k, v in res.items()})
