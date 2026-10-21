
var JUSTIFY = {left: Justification.LEFT_ALIGN, center: Justification.CENTER_ALIGN,
               right: Justification.RIGHT_ALIGN, justify: Justification.LEFT_JUSTIFIED};
var missing = {};

function setp(obj, name, value) {
    try { obj[name] = value; return true; } catch (e) { return false; }
}

function styleRun(r, run, leading) {
    if (run.font && !setp(r, "appliedFont", run.font)) missing[run.font] = true;
    if (!setp(r, "fontStyle", run.style) && run.style != "Regular") missing[run.font + " " + run.style] = true;
    setp(r, "pointSize", run.size);
    setp(r, "leading", leading ? leading : Leading.AUTO);
    if (run.u) setp(r, "underline", true);
}

function stylePara(r, p) {
    setp(r, "justification", JUSTIFY[p.align] || Justification.LEFT_ALIGN);
    setp(r, "leftIndent", p.left);
    setp(r, "rightIndent", p.right);
    setp(r, "firstLineIndent", p.indent);
    setp(r, "spaceBefore", p.before);
    setp(r, "spaceAfter", p.after);
    setp(r, "hyphenation", false);
}

// Puts paragraphs into a story or a table cell and formats them.
function fill(target, paras) {
    var texts = [], i, j;
    for (i = 0; i < paras.length; i++) {
        var t = "";
        for (j = 0; j < paras[i].runs.length; j++) t += paras[i].runs[j].t;
        texts.push(t);
    }
    target.contents = texts.join("\r");
    var chars = target.characters, pos = 0;
    for (i = 0; i < paras.length; i++) {
        var p = paras[i], start = pos;
        for (j = 0; j < p.runs.length; j++) {
            var len = p.runs[j].t.length;
            if (len) styleRun(chars.itemByRange(pos, pos + len - 1), p.runs[j], p.leading);
            pos += len;
        }
        var last = i < paras.length - 1 ? pos : pos - 1;  // with its return
        if (last >= start) {
            var range = chars.itemByRange(start, last);
            stylePara(range, p);
            if (pos == start && p.runs.length) styleRun(range, p.runs[0], p.leading);  // blank line
        }
        pos += 1;
    }
}

function textFrame(page, x, y, w, h) {
    var tf = page.textFrames.add();
    tf.geometricBounds = [y, x, y + h, x + w];
    setp(tf.textFramePreferences, "insetSpacing", [0, 0, 0, 0]);
    setp(tf, "strokeWeight", 0);
    return tf;
}

function drawLines(page, lines, black, none) {
    for (var i = 0; i < lines.length; i++) {
        var l = lines[i];
        var item = l.closed ? page.polygons.add() : page.graphicLines.add();
        item.paths.item(0).entirePath = l.pts;
        setp(item, "strokeWeight", l.sw);
        setp(item, "strokeColor", black);
        setp(item, "fillColor", l.fill ? black : none);
    }
}

function drawTable(page, t, black) {
    var tf = textFrame(page, t.x, t.y, t.w + 1, Math.max(t.h + 1, page.bounds[2] - t.y));
    var table = tf.insertionPoints.item(0).tables.add();
    table.bodyRowCount = t.rows.length;
    table.columnCount = t.cols.length;
    var i;
    for (i = 0; i < t.cols.length; i++) table.columns.item(i).width = t.cols[i];
    var spans = [];
    for (i = 0; i < t.cells.length; i++) {
        var c = t.cells[i];
        if (c.rs > 1 || c.cs > 1) spans.push(c);
    }
    for (i = 0; i < spans.length; i++) {
        var s = spans[i];
        table.cells.item(s.c + ":" + s.r).merge(table.cells.item((s.c + s.cs - 1) + ":" + (s.r + s.rs - 1)));
    }
    var rows = t.rows.length, cols = t.cols.length;
    for (i = 0; i < t.cells.length; i++) {
        var d = t.cells[i];
        var cell = table.cells.item(d.c + ":" + d.r);
        var side = 0;
        for (var k = d.c; k < d.c + d.cs; k++) side += t.cols[k];
        side = side < 30 ? 0.5 : 1.5;
        setp(cell, "topInset", 1);
        setp(cell, "bottomInset", 1);
        setp(cell, "leftInset", side);
        setp(cell, "rightInset", side);
        setp(cell, "verticalJustification", VerticalJustification.CENTER_ALIGN);
        var edges = {top: d.r == 0, left: d.c == 0, bottom: d.r + d.rs == rows, right: d.c + d.cs == cols};
        for (var e in edges) {
            setp(cell, e + "EdgeStrokeWeight", edges[e] ? t.border : t.rule);
            setp(cell, e + "EdgeStrokeColor", black);
        }
        if (d.rotate) setp(cell, "rotationAngle", d.rotate);
        if (d.paras.length) fill(cell, d.paras);
    }
    for (i = 0; i < t.rows.length; i++) setp(table.rows.item(i), "height", t.rows[i]);
    try { tf.fit(FitOptions.FRAME_TO_CONTENT); } catch (e) {}
}

function build(DATA) {
    var doc = app.documents.add();
    var vp = doc.viewPreferences;
    vp.horizontalMeasurementUnits = MeasurementUnits.POINTS;
    vp.verticalMeasurementUnits = MeasurementUnits.POINTS;
    vp.rulerOrigin = RulerOrigin.PAGE_ORIGIN;
    doc.zeroPoint = [0, 0];
    var dp = doc.documentPreferences;
    dp.facingPages = false;
    dp.pageWidth = DATA.pages[0].w;
    dp.pageHeight = DATA.pages[0].h;
    dp.pagesPerDocument = DATA.pages.length;
    try { app.activeWindow.transformReferencePoint = AnchorPoint.CENTER_ANCHOR; } catch (e) {}
    var black = doc.swatches.item("Black"), none = doc.swatches.item("None");

    var chains = {}, order = [], grow = [], i, j;
    for (i = 0; i < DATA.pages.length; i++) {
        var pd = DATA.pages[i], page = doc.pages.item(i);
        drawLines(page, pd.lines, black, none);
        for (j = 0; j < pd.frames.length; j++) {
            var f = pd.frames[j];
            var tf = textFrame(page, f.x, f.y, f.w, f.h > 0 ? f.h : 27);
            if (f.rotate) setp(tf, "rotationAngle", -f.rotate);
            if (!(f.h > 0)) grow.push(tf);
            if (!chains[f.story]) { chains[f.story] = []; order.push(f.story); }
            chains[f.story].push(tf);
        }
        for (j = 0; j < pd.tables.length; j++) drawTable(page, pd.tables[j], black);
    }
    for (i = 0; i < order.length; i++) {
        var chain = chains[order[i]];
        for (j = 0; j + 1 < chain.length; j++) chain[j].nextTextFrame = chain[j + 1];
        var paras = DATA.stories[order[i]];
        if (paras && paras.length) fill(chain[0].parentStory, paras);
    }
    for (i = 0; i < grow.length; i++) { try { grow[i].fit(FitOptions.FRAME_TO_CONTENT); } catch (e) {} }
    return doc;
}

function fileName(title) { return title.replace(/[\\\/:*?"<>|]/g, "").replace(/^\s+|\s+$/g, "") || "document"; }

// A file in `folder` named after `title` that isn't there yet.
function freeFile(folder, title) {
    var out = new File(folder + "/" + title + ".indd");
    for (var n = 2; out.exists; n++) out = new File(folder + "/" + title + " (" + n + ").indd");
    return out;
}

var level = app.scriptPreferences.userInteractionLevel;
app.scriptPreferences.userInteractionLevel = UserInteractionLevels.NEVER_INTERACT;
var saved = [], failed = [], folder = Folder.desktop;
try {
    if (DOCS.length > 1) {
        // Many files go together in one folder on the desktop.
        folder = new Folder(Folder.desktop + "/PageMaker to InDesign");
        if (!folder.exists) folder.create();
    }
    for (var d = 0; d < DOCS.length; d++) {
        var built = null;
        try {
            built = build(DOCS[d].data);
            var out = freeFile(folder, fileName(DOCS[d].title));
            built.save(out);
            saved.push(decodeURI(out.name));
            if (DOCS.length > 1) { built.close(SaveOptions.NO); built = null; }
        } catch (e) {
            failed.push(DOCS[d].title + ": " + e.message);
            if (built && DOCS.length > 1) { try { built.close(SaveOptions.NO); } catch (e2) {} }
        }
    }
} finally {
    app.scriptPreferences.userInteractionLevel = level;
}
var gone = [];
for (var name in missing) gone.push(name);
var where = DOCS.length > 1 ? "the \"PageMaker to InDesign\" folder on your desktop" : "your desktop";
alert((saved.length == 1 ? "Saved as " + saved[0] + " on " + where + "." :
       saved.length ? "Saved " + saved.length + " InDesign files in " + where + "." :
       "Nothing could be saved.") +
      (failed.length ? "\n\nThese didn't convert:\n" + failed.join("\n") : "") +
      (SKIPPED.length ? "\n\nThese couldn't be read on the website:\n" + SKIPPED.join("\n") : "") +
      (gone.length ? "\n\nThese fonts aren't installed, so InDesign marks them as missing:\n" + gone.join("\n") : ""));
