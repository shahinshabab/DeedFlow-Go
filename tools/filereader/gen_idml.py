"""Generates random (and sometimes damaged) IDML files for comparing the Go
reader with the Django project's.   python gen_idml.py OUTDIR COUNT SEED"""

import io
import math
import os
import random
import sys
import zipfile

PKG = 'xmlns:idPkg="http://ns.adobe.com/AdobeInDesign/idml/1.0/packaging"'
FONTS = ["ML-TTKarthika", "Times New Roman", "Arial Baltic", "FML-TTIndulekha", "Minion Pro"]
TEXTS = ["B[mcw", "sImÃw", "\\mfw", "Sale deed", "Survey No. 123/4A", "കൊല്ലം", "R&amp;D &lt;x&gt;", "  lead", "tail  ",
         "1234", "Page <?ACE 18?>", "a<?ACE 8?>b", "x<?ACE 7?>y", "line"]


def esc_attr(v):
    return str(v).replace("&", "&amp;").replace('"', "&quot;")


def pts_xml(pts, open_=False):
    p = "".join(f'<PathPointType Anchor="{x} {y}" LeftDirection="{x} {y}" RightDirection="{x} {y}"/>' for x, y in pts)
    return (f'<Properties><PathGeometry><GeometryPathType PathOpen="{"true" if open_ else "false"}">'
            f'<PathPointArray>{p}</PathPointArray></GeometryPathType></PathGeometry></Properties>')


def transform(rng, allow_rot=True):
    r = rng.random()
    if r < .6:
        return "1 0 0 1 %s %s" % (round(rng.uniform(-50, 50), 1), round(rng.uniform(-50, 50), 1))
    if r < .75 and allow_rot:
        a = math.radians(rng.choice([90, 180, 270, 30, -45]))
        return "%s %s %s %s %s %s" % (round(math.cos(a), 6), round(math.sin(a), 6), round(-math.sin(a), 6),
                                      round(math.cos(a), 6), rng.randint(-20, 200), rng.randint(-20, 200))
    if r < .85:
        return "%s 0 0 %s 0 0" % (rng.choice([0.5, 2, 1.25]), rng.choice([0.5, 1, 2]))
    return rng.choice(["garbage", "", "1 0 0 1", "1 0 0 1 0 0 0"])


def story_xml(rng, sid, styles):
    out = []
    for _ in range(rng.randint(1, 3)):
        ps = rng.choice(styles["para"] + [None, "ParagraphStyle/Missing"])
        attrs = ""
        if ps:
            attrs += f' AppliedParagraphStyle="{ps}"'
        if rng.random() < .5:
            attrs += f' Justification="{rng.choice(["LeftAlign", "CenterAlign", "RightAlign", "FullyJustified", "ToBindingSide", "Bogus"])}"'
        if rng.random() < .3:
            attrs += f' FirstLineIndent="{rng.choice([0, 12, "x"])}" SpaceBefore="{rng.choice([0, 6])}" Leading="{rng.choice(["Auto", "14", "bad"])}"'
        body = ""
        for _ in range(rng.randint(0, 3)):
            cs = rng.choice(styles["char"] + [None])
            cattrs = f' AppliedCharacterStyle="{cs}"' if cs else ""
            if rng.random() < .5:
                cattrs += f' PointSize="{rng.choice([9, 10, 12, 18, "z"])}"'
            if rng.random() < .3:
                cattrs += f' FontStyle="{rng.choice(["Bold", "Italic", "Bold Italic", "Black", "Oblique", "Regular"])}"'
            if rng.random() < .3:
                cattrs += f' FillColor="{rng.choice(["Color/Red", "Color/Black", "Swatch/None", "Color/Missing", "n"])}"'
            if rng.random() < .2:
                cattrs += ' Underline="true"'
            props = ""
            if rng.random() < .5:
                props = f'<Properties><AppliedFont type="string">{rng.choice(FONTS)}</AppliedFont></Properties>'
            inner = ""
            for _ in range(rng.randint(0, 3)):
                k = rng.random()
                if k < .6:
                    inner += f'<Content>{rng.choice(TEXTS)}</Content>'
                elif k < .85:
                    inner += "<Br/>"
                else:
                    inner += table_xml(rng, styles)
            c = f'<CharacterStyleRange{cattrs}>{props}{inner}</CharacterStyleRange>'
            if rng.random() < .15:
                c = f'<XMLElement Self="x{rng.randint(1,99)}">{c}</XMLElement>'
            body += c
        p = f'<ParagraphStyleRange{attrs}>{body}</ParagraphStyleRange>'
        if rng.random() < .1:
            p = f'<XMLElement Self="y1">{p}</XMLElement>'
        out.append(p)
    return f'<?xml version="1.0" encoding="UTF-8"?><idPkg:Story {PKG}><Story Self="{sid}">{"".join(out)}</Story></idPkg:Story>'


def table_xml(rng, styles):
    nr, nc = rng.randint(1, 3), rng.randint(1, 3)
    cols = "".join(f'<Column Self="c{i}" Name="{i}" SingleColumnWidth="{rng.choice([60, 100, 150])}"/>' for i in range(nc))
    rows = "".join(f'<Row Self="r{i}" Name="{i}"/>' for i in range(nr)) if rng.random() < .9 else ""
    cells = ""
    for r in range(nr):
        for c in range(nc):
            if rng.random() < .85:
                cs, rs = rng.choice([1, 1, 2]), rng.choice([1, 1, 2])
                rot = f' RotationAngle="{rng.choice([0, 90, 270])}"' if rng.random() < .2 else ""
                name = f"{c}:{r}" if rng.random() < .95 else rng.choice(["bad", "1:1:1", "0:9"])
                cells += (f'<Cell Self="x{r}{c}" Name="{name}" RowSpan="{rs}" ColumnSpan="{cs}"{rot}>'
                          f'<ParagraphStyleRange><CharacterStyleRange><Content>{rng.choice(TEXTS)}</Content></CharacterStyleRange>'
                          f'</ParagraphStyleRange></Cell>')
    return f'<Table Self="t" HeaderRowCount="0" BodyRowCount="{nr}" ColumnCount="{nc}">{rows}{cols}{cells}</Table>'


def frame(rng, tag, idx, box, stories, inner=""):
    x0, y0, x1, y1 = box
    attrs = f'Self="f{idx}" ItemTransform="{transform(rng)}"'
    if rng.random() < .1:
        attrs += ' Visible="false"'
    if tag == "TextFrame":
        attrs += f' ParentStory="{rng.choice(stories + ["missing"])}"'
        r = rng.random()
        if r < .3:
            pref = f'<TextFramePreference InsetSpacing="{rng.choice(["4", "1 2 3 4", "2 3", ""])}" VerticalJustification="{rng.choice(["CenterAlign", "BottomAlign", "TopAlign"])}"/>'
        elif r < .5:
            pref = ('<TextFramePreference><Properties><InsetSpacing type="list"><ListItem type="unit">3</ListItem>'
                    '<ListItem type="unit">4</ListItem><ListItem type="unit">5</ListItem><ListItem type="unit">6</ListItem>'
                    '</InsetSpacing></Properties></TextFramePreference>')
        else:
            pref = ""
        inner += pref
    else:
        attrs += f' StrokeColor="{rng.choice(["Color/Black", "Color/Red", "Swatch/None", "Color/Nope"])}"'
        attrs += f' StrokeWeight="{rng.choice([0, 0.5, 1, 3, "x"])}" FillColor="{rng.choice(["Swatch/None", "Color/Red", "Color/Paper"])}"'
    if tag == "GraphicLine":
        pts = [(x0, y0), (x1, y1)]
        return f'<{tag} {attrs}>{pts_xml(pts, True)}{inner}</{tag}>'
    if tag == "Polygon" and rng.random() < .5:
        pts = [(x0, y0), (x1, y0), ((x0 + x1) / 2, y1)]
        return f'<{tag} {attrs}>{pts_xml(pts, rng.random() < .3)}{inner}</{tag}>'
    pts = [(x0, y0), (x0, y1), (x1, y1), (x1, y0)]
    if tag == "Rectangle" and rng.random() < .15:
        inner += f'<Image Self="img{idx}"><Link LinkResourceURI="file:/C:/pics/photo{idx}.tif"/></Image>'
    return f'<{tag} {attrs}>{pts_xml(pts)}{inner}</{tag}>'


def items(rng, stories, W, H, n0):
    out = []
    for i in range(rng.randint(0, 6)):
        x0, y0 = rng.uniform(0, W - 80), rng.uniform(-H / 2, H / 2 - 80)
        box = (round(x0, 1), round(y0, 1), round(x0 + rng.uniform(20, 200), 1), round(y0 + rng.uniform(20, 300), 1))
        tag = rng.choice(["TextFrame", "TextFrame", "TextFrame", "Rectangle", "Oval", "Polygon", "GraphicLine", "Group"])
        if tag == "Group":
            inner = "".join(frame(rng, rng.choice(["TextFrame", "Rectangle"]), n0 + i * 10 + k, box, stories) for k in range(2))
            out.append(f'<Group Self="g{n0+i}" ItemTransform="{transform(rng)}">{inner}</Group>')
        else:
            out.append(frame(rng, tag, n0 + i, box, stories))
    return out


def build(rng):
    W, H = 595.28, 841.89
    stories = ["u%d" % (10 + i) for i in range(rng.randint(1, 4))]
    styles = {"para": ["ParagraphStyle/Body", "ParagraphStyle/Cycle", "ParagraphStyle/Normal"],
              "char": ["CharacterStyle/Bold", "CharacterStyle/Missing"]}
    files = {"mimetype": "application/vnd.adobe.indesign-idml-package"}
    master_refs, masters = [], []
    for m in range(rng.randint(0, 2)):
        facing = rng.random() < .5
        pages = ""
        if facing:
            pages += f'<Page Self="mpl{m}" GeometricBounds="0 0 {H} {W}" ItemTransform="1 0 0 1 {-W} {-H/2}"/>'
        pages += f'<Page Self="mpr{m}" GeometricBounds="0 0 {H} {W}" ItemTransform="1 0 0 1 0 {-H/2}"/>'
        body = "".join(items(rng, stories, W, H, 500 + m * 50))
        files[f"MasterSpreads/MasterSpread_m{m}.xml"] = (f'<?xml version="1.0"?><idPkg:MasterSpread {PKG}><MasterSpread Self="m{m}" '
                                                       f'ItemTransform="1 0 0 1 0 0">{pages}{body}</MasterSpread></idPkg:MasterSpread>')
        master_refs.append(f'<idPkg:MasterSpread src="MasterSpreads/MasterSpread_m{m}.xml"/>')
        masters.append(f"m{m}")
    spread_refs = []
    for s in range(rng.randint(1, 3)):
        facing = rng.random() < .4
        pages = ""
        am = rng.choice(masters + ["nomaster"]) if masters or rng.random() < .3 else "n"
        if facing:
            pages += f'<Page Self="pl{s}" Name="{2*s}" AppliedMaster="{am}" GeometricBounds="0 0 {H} {W}" ItemTransform="1 0 0 1 {-W} {-H/2}"/>'
        pages += f'<Page Self="pr{s}" Name="{2*s+1}" AppliedMaster="{am}" GeometricBounds="0 0 {H} {W}" ItemTransform="1 0 0 1 0 {-H/2}"/>'
        body = "".join(items(rng, stories, W * (2 if facing else 1), H, 100 + s * 50))
        files[f"Spreads/Spread_s{s}.xml"] = (f'<?xml version="1.0"?><idPkg:Spread {PKG}><Spread Self="s{s}" '
                                           f'ItemTransform="{rng.choice(["1 0 0 1 0 0", "1 0 0 1 0 900"])}">{pages}{body}</Spread></idPkg:Spread>')
        spread_refs.append(f'<idPkg:Spread src="Spreads/Spread_s{s}.xml"/>')
    story_refs = []
    for sid in stories:
        files[f"Stories/Story_{sid}.xml"] = story_xml(rng, sid, styles)
        story_refs.append(f'<idPkg:Story src="Stories/Story_{sid}.xml"/>')
    files["designmap.xml"] = (f'<?xml version="1.0"?><Document {PKG} DOMVersion="16.0" Self="d"><idPkg:Graphic src="Resources/Graphic.xml"/>'
                              f'<idPkg:Styles src="Resources/Styles.xml"/>{"".join(master_refs)}{"".join(spread_refs)}{"".join(story_refs)}</Document>')
    files["Resources/Graphic.xml"] = (f'<idPkg:Graphic {PKG}><Color Self="Color/Black" Space="CMYK" ColorValue="0 0 0 100"/>'
                                      f'<Color Self="Color/Red" Space="RGB" ColorValue="200 0 {rng.choice([0, 300, -5])}"/>'
                                      f'<Color Self="Color/Cyan" Space="CMYK" ColorValue="100 0 0 0"/><Color Self="Color/Bad" Space="LAB" ColorValue="1 2 3"/></idPkg:Graphic>')
    files["Resources/Styles.xml"] = (f'<idPkg:Styles {PKG}><RootParagraphStyleGroup>'
                                     '<ParagraphStyle Self="ParagraphStyle/Normal" PointSize="12" Justification="LeftAlign"><Properties><AppliedFont type="string">Minion Pro</AppliedFont><Leading type="enumeration">Auto</Leading></Properties></ParagraphStyle>'
                                     '<ParagraphStyle Self="ParagraphStyle/Body" SpaceAfter="6" LeftIndent="10"><Properties><BasedOn type="object">ParagraphStyle/Normal</BasedOn><AppliedFont type="string">ML-TTKarthika</AppliedFont><Leading type="unit">15</Leading></Properties></ParagraphStyle>'
                                     '<ParagraphStyle Self="ParagraphStyle/Cycle"><Properties><BasedOn type="object">ParagraphStyle/Cycle</BasedOn></Properties></ParagraphStyle>'
                                     '</RootParagraphStyleGroup><RootCharacterStyleGroup><CharacterStyle Self="CharacterStyle/Bold" FontStyle="Bold"/></RootCharacterStyleGroup></idPkg:Styles>')
    return files


def damage(rng, files):
    k = rng.random()
    names = list(files)
    if k < .3:
        n = rng.choice([x for x in names if x != "mimetype"])
        files[n] = files[n][:rng.randint(0, len(files[n]))]
    elif k < .5:
        del files[rng.choice([x for x in names if x != "mimetype"])]
    elif k < .7:
        n = rng.choice(names)
        s = files[n]
        if s:
            i = rng.randrange(len(s))
            files[n] = s[:i] + rng.choice(["", "<", "&", "\ufffd"]) + s[i + 1:]
    return files


if __name__ == "__main__":
    out, count, seed = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
    os.makedirs(out, exist_ok=True)
    for i in range(count):
        rng = random.Random(seed * 7919 + i)
        files = build(rng)
        if rng.random() < .25:
            files = damage(rng, files)
        buf = io.BytesIO()
        with zipfile.ZipFile(buf, "w") as z:
            for n, c in files.items():
                z.writestr(n, c)
        open(os.path.join(out, "g%04d.idml" % i), "wb").write(buf.getvalue())
