"""Builds a small deed-like PDF (a text page and a 21-row schedule table
page) for the pipeline integration test.  Usage: pipeline_pdf.py OUT.pdf"""
import sys
from reportlab.lib.pagesizes import A4
from reportlab.pdfgen import canvas

c = canvas.Canvas(sys.argv[1], pagesize=A4)
c.setFont("Helvetica", 11)
y = 800
for line in ["Sale deed between Anil Kumar and Latha Devi", "Property: Residential plot with Panchayath road access",
             "Price Rs. 1,25,000"]:
    c.drawString(72, y, line)
    y -= 16
c.showPage()
rows = 21
x = [40, 80, 160, 260, 360, 560]
top = 800
h = 30
for i in range(rows + 1):
    c.line(x[0], top - i * h, x[-1], top - i * h)
for xx in x:
    c.line(xx, top, xx, top - rows * h)
c.setFont("Helvetica", 7)
vals = {0: "1", 1: "Palakkad", 2: "Vilayur", 3: "Pattambi", 4: "Koppam", 5: "Kizhumuri", 6: "Koppam", 7: "......",
        8: "12", 9: "3", 10: "45 46", 11: "1 2", 12: "10.5 BÀ", 13: "", 14: "12/2020", 15: ""}
for r in range(rows):
    col = 4
    if r == 16:
        col = 2
    if r >= 17:
        col = 3
    text = vals.get(r, "boundary %d" % r if r >= 17 else "x")
    c.drawString(x[col] + 3, top - r * h - 18, text)
    c.drawString(x[0] + 3, top - r * h - 18, "r%d" % r)
c.showPage()
c.save()
