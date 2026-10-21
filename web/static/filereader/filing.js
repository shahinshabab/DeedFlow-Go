// Filing sheet: writes the deed's text (see filereader/filing.py for its
// shape) out on ruled pages like a notebook, editable in place, and prints
// them with the margins, lines and font chosen in the toolbar.
//
// The pages are columns of one multi-column box, set side by side: each
// column is as tall as a page's lines less its last one (which stays
// blank), and a blank line before the text leaves the first page's first
// line blank too. Every line of text is one line gap tall, so it sits on
// its ruled line. For printing, each page is a window onto its own column
// of a copy of that box.
//
// ML-TT Karthika only draws its own legacy glyph codes, not Unicode
// Malayalam, so choosing it turns the sheet's text into those codes on the
// server (and back again when another font is chosen). Latin words, which
// that font would draw as Malayalam, are wrapped in .fs-latin spans meanwhile.
//
// The sheet is saved on the server (its HTML and settings) with the Save
// button, and on its own every minute while there are unsaved changes.
// Opened to view, it is read only and shows the lines only if they print.
//
// Straight lines can be drawn on the pages too, across or down, like a
// table's: drawn, moved and stretched with the mouse. They belong to a page
// (in mm from its corner), so they stay put while the text flows, and they
// always print. A table's own column lines can be turned into such lines.
(function () {
  "use strict";

  var dataEl = document.getElementById("fs-doc");
  var content = document.getElementById("fsContent");
  if (!dataEl || !content) return;
  var doc = JSON.parse(dataEl.textContent);
  var root = document.documentElement.style;
  var desk = document.getElementById("fsDesk");
  var scaler = document.getElementById("fsScaler");
  var strip = document.getElementById("fsStrip");
  var sheets = document.getElementById("fsSheets");
  var printPages = document.getElementById("fsPrintPages");
  var note = document.getElementById("fsNote");
  var pageStyle = document.getElementById("fsPageStyle");
  var MM_PER_PT = 25.4 / 72;
  var PX_PER_MM = 96 / 25.4;
  var SPACE = 8;  // mm between pages on screen
  var RULE = "#8fb4e6";
  var MARGIN_RULE = "#e05a5a";
  var PAPERS = { a4: [210, 297, "A4"], legal: [215.9, 355.6, "legal"], letter: [215.9, 279.4, "letter"] };
  var FONTS = {
    karthika: '"ML-TT Karthika", "Noto Serif Malayalam", serif',
    serif: '"Noto Serif Malayalam", "Noto Sans Malayalam", serif',
    sans: '"Noto Sans Malayalam", "Noto Serif Malayalam", sans-serif'
  };
  var ids = { top: "fsTop", bottom: "fsBottom", left: "fsLeft", right: "fsRight", topEven: "fsTopEven",
              bottomEven: "fsBottomEven", leftEven: "fsLeftEven", rightEven: "fsRightEven", lines: "fsLines",
              gap: "fsGap", lift: "fsLift", printLines: "fsPrintLines", size: "fsSize", font: "fsFont",
              paper: "fsPaper", zoom: "fsZoom" };
  var addPage = document.getElementById("fsAddPage");
  var removePage = document.getElementById("fsRemovePage");
  var pageInfo = document.getElementById("fsPageInfo");
  var fields = {};
  Object.keys(ids).forEach(function (k) { fields[k] = document.getElementById(ids[k]); });
  var mode = "unicode";     // what the text in the sheet is: "unicode" or "legacy"
  var edited = false;
  var busy = false;
  var layout = null;
  var drawn = [];        // lines drawn on the pages: {p: page, h: across?, x, y, len} in mm
  var selected = -1;     // index in drawn of the line picked to move or stretch

  function el(tag, cls) {
    var node = document.createElement(tag);
    if (cls) node.className = cls;
    return node;
  }

  function round(v, step) { return Math.round(v / step) * step; }

  // --- the text ---------------------------------------------------------------

  function renderParagraph(b) {
    var p = el("p");
    if (b.align && b.align !== "left") p.style.textAlign = b.align;
    b.spans.forEach(function (s) {
      var node = el(s.bold ? "b" : "span");
      if (s.italic) node.style.fontStyle = "italic";
      node.textContent = s.t;
      p.appendChild(node);
    });
    return p;
  }

  function cellText(cell) {
    return cell.blocks.map(function (p) {
      return p.spans.map(function (s) { return s.t; }).join("");
    }).join(" ").trim();
  }

  function renderTable(b) {
    // A first column of serial numbers has no line after it; otherwise
    // the lines run between every two columns. There's none on the outside.
    var serial = b.rows.every(function (row) {
      return row.every(function (cell) { return cell.col !== 0 || /^\d*\.?$/.test(cellText(cell)); });
    });
    var firstLine = serial ? 2 : 1;
    var table = el("table", "fs-table");
    var colgroup = el("colgroup");
    b.cols.forEach(function (w) {
      var col = el("col");
      // A serial-number column keeps room for two digits.
      col.style.width = w < 6 ? "2.2em" : w + "%";
      colgroup.appendChild(col);
    });
    table.appendChild(colgroup);
    var tbody = el("tbody");
    b.rows.forEach(function (row) {
      var tr = el("tr");
      row.forEach(function (cell) {
        var td = el("td", cell.col >= firstLine ? "fs-vline" : null);
        if (cell.colspan > 1) td.colSpan = cell.colspan;
        if (cell.rowspan > 1) td.rowSpan = cell.rowspan;
        if (cell.align && cell.align !== "left") td.style.textAlign = cell.align;
        if (cell.blocks.length) cell.blocks.forEach(function (p) { td.appendChild(renderParagraph(p)); });
        else td.appendChild(el("p")).appendChild(document.createElement("br"));
        tr.appendChild(td);
      });
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    return table;
  }

  function render() {
    content.textContent = "";
    doc.blocks.forEach(function (b) {
      content.appendChild(b.kind === "table" ? renderTable(b) : renderParagraph(b));
    });
    if (!content.childNodes.length) content.appendChild(el("p")).appendChild(document.createElement("br"));
    mode = "unicode";
    edited = false;
  }

  function textNodes(skipLatin) {
    var out = [];
    var walker = document.createTreeWalker(content, NodeFilter.SHOW_TEXT, {
      acceptNode: function (n) {
        if (!n.nodeValue) return NodeFilter.FILTER_REJECT;
        if (skipLatin && n.parentNode.closest(".fs-latin")) return NodeFilter.FILTER_REJECT;
        return NodeFilter.FILTER_ACCEPT;
      }
    });
    while (walker.nextNode()) out.push(walker.currentNode);
    return out;
  }

  function convert(to, texts) {
    return fetch(window.FS_CONVERT_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-CSRFToken": window.FS_CSRF },
      body: JSON.stringify({ to: to, texts: texts }),
      credentials: "same-origin"
    }).then(function (r) {
      if (!r.ok) throw new Error("convert failed");
      return r.json();
    }).then(function (data) { return data.texts; });
  }

  // Turns the sheet's text into the legacy font's codes, or back.
  function setMode(want) {
    if (want === mode) return Promise.resolve();
    var nodes = textNodes(want === "unicode");
    var keepEdited = edited;
    return convert(want, nodes.map(function (n) { return n.nodeValue; })).then(function (texts) {
      nodes.forEach(function (node, i) {
        if (want === "unicode") {
          node.nodeValue = texts[i];
          return;
        }
        var frag = document.createDocumentFragment();
        texts[i].forEach(function (piece) {
          if (piece[1]) {
            frag.appendChild(document.createTextNode(piece[0]));
          } else {
            var span = el("span", "fs-latin");
            span.textContent = piece[0];
            frag.appendChild(span);
          }
        });
        node.parentNode.replaceChild(frag, node);
      });
      if (want === "unicode") {
        content.querySelectorAll(".fs-latin").forEach(function (span) {
          span.replaceWith(document.createTextNode(span.textContent));
        });
        content.normalize();
      }
      mode = want;
      edited = keepEdited;
    });
  }

  // --- settings -----------------------------------------------------------------

  function number(input, fallback, lo, hi) {
    var v = parseFloat(input.value);
    if (isNaN(v)) return fallback;
    return Math.min(hi, Math.max(lo, v));
  }

  function paper() { return PAPERS[fields.paper.value] || PAPERS.a4; }

  // The bottom margins and the number of lines go together: a page's
  // bottom margin is what its lines leave below them. Changing either
  // bottom margin sets the number of lines, and that sets both bottoms.
  function linesFromBottom(even) {
    var h = paper()[1], gap = number(fields.gap, 8, 3, 30);
    var top = number(even ? fields.topEven : fields.top, 25, 0, 150);
    var bottom = number(even ? fields.bottomEven : fields.bottom, 25, 0, 150);
    fields.lines.value = Math.max(3, Math.floor((h - top - bottom) / gap + 1e-6));
  }

  function bottomFromLines() {
    var h = paper()[1], gap = number(fields.gap, 8, 3, 30);
    var lines = Math.round(number(fields.lines, 30, 3, 120));
    fields.bottom.value = round(Math.max(0, h - number(fields.top, 25, 0, 150) - lines * gap), 0.5);
    fields.bottomEven.value = round(Math.max(0, h - number(fields.topEven, 25, 0, 150) - lines * gap), 0.5);
  }

  // Odd pages (1, 3, ...) and even pages each have their own margins. Every
  // page holds the same width of text, as one column of the sheet, so the
  // even pages' right margin follows from the rest.
  function readSettings() {
    var p = paper();
    var s = {
      w: p[0], h: p[1], name: p[2], lines: Math.round(number(fields.lines, 30, 3, 120)),
      gap: number(fields.gap, 8, 3, 30), lift: number(fields.lift, 1.5, 0, 10), size: number(fields.size, 14, 6, 48)
    };
    var odd = { top: number(fields.top, 25, 0, 150), left: number(fields.left, 25, 0, 100) };
    s.colW = Math.max(20, s.w - odd.left - number(fields.right, 20, 0, 100));
    var even = { top: number(fields.topEven, 25, 0, 150),
                 left: Math.min(number(fields.leftEven, 25, 0, 100), s.w - s.colW) };
    fields.rightEven.value = round(s.w - even.left - s.colW, 0.5);
    s.margins = [odd, even];
    // The text's column sits as far in as the larger margin; each sheet of
    // paper is placed so that its own margins fall around its column.
    s.cLeft = Math.max(odd.left, even.left);
    s.cTop = Math.max(odd.top, even.top);
    s.pitch = s.w + SPACE + Math.abs(odd.left - even.left);
    return s;
  }

  function margins(k) { return layout.margins[k % 2]; }

  function sheetX(k) { return layout.cLeft + k * layout.pitch - margins(k).left; }

  // Where the letters' baseline falls in a line one gap tall, so the text
  // can be moved to sit `lift` above its ruled line.
  var probe = null;
  function baseline(s) {
    if (!probe) {
      probe = el("div");
      probe.style.cssText = "position:absolute;left:-9999px;top:0;visibility:hidden;white-space:nowrap";
      probe.innerHTML = 'മല Ag<span style="display:inline-block;width:0;height:0;vertical-align:baseline"></span>';
      document.body.appendChild(probe);
    }
    probe.style.fontFamily = FONTS[fields.font.value] || FONTS.serif;
    probe.style.fontSize = s.size + "pt";
    probe.style.lineHeight = s.gap + "mm";
    var mark = probe.lastChild.getBoundingClientRect();
    var box = probe.getBoundingClientRect();
    return (mark.top - box.top) / PX_PER_MM;  // mm from the top of the line
  }

  function apply() {
    var s = readSettings();
    var shift = (s.gap - s.lift) - baseline(s);
    var vars = {
      "--fs-paper-w": s.w + "mm", "--fs-paper-h": s.h + "mm", "--fs-c-top": s.cTop + "mm",
      "--fs-c-left": s.cLeft + "mm", "--fs-gap": s.gap + "mm", "--fs-lines": s.lines,
      "--fs-col-w": s.colW + "mm", "--fs-col-gap": (s.pitch - s.colW) + "mm",
      "--fs-shift": shift.toFixed(3) + "mm", "--fs-size": s.size + "pt", "--fs-rule": RULE,
      "--fs-font": FONTS[fields.font.value] || FONTS.serif
    };
    Object.keys(vars).forEach(function (k) { root.setProperty(k, vars[k]); });
    pageStyle.textContent = "@page { size: " + s.name + "; margin: 0; }";
    layout = s;
    // Viewing shows the sheet as it prints.
    strip.classList.toggle("fs-no-rules", !!window.FS_VIEW && !fields.printLines.checked);
    paginate();
  }

  function pageCount() {
    var range = document.createRange();
    range.selectNodeContents(content);
    var rects = range.getClientRects();
    var box = content.getBoundingClientRect();
    var zoom = box.width / (layout.colW * PX_PER_MM) || 1;
    var right = 0;
    for (var i = 0; i < rects.length; i++) {
      if (rects[i].width || rects[i].height) right = Math.max(right, rects[i].right - box.left);
    }
    return Math.max(1, Math.floor((right / zoom / PX_PER_MM - 0.5) / layout.pitch) + 1);
  }

  // The ruled lines are drawn, not a background, since browsers leave
  // backgrounds out of print by default. Red lines mark the page's top,
  // left and right margins.
  function rules(k) {
    var NS = "http://www.w3.org/2000/svg";
    var m = margins(k);
    var svg = document.createElementNS(NS, "svg");
    svg.setAttribute("class", "fs-rules");
    svg.setAttribute("viewBox", "0 0 " + layout.w + " " + layout.h);
    svg.setAttribute("preserveAspectRatio", "none");
    function line(x1, y1, x2, y2, color) {
      var node = document.createElementNS(NS, "line");
      node.setAttribute("x1", x1);
      node.setAttribute("y1", y1);
      node.setAttribute("x2", x2);
      node.setAttribute("y2", y2);
      node.setAttribute("stroke", color);
      svg.appendChild(node);
    }
    for (var n = 1; n <= layout.lines; n++) {
      var y = m.top + n * layout.gap;
      if (y > layout.h) break;
      line(0, y, layout.w, y, RULE);
    }
    line(0, m.top, layout.w, m.top, MARGIN_RULE);
    line(m.left, 0, m.left, layout.h, MARGIN_RULE);
    line(m.left + layout.colW, 0, m.left + layout.colW, layout.h, MARGIN_RULE);
    return svg;
  }

  function pageNode(k, total, withRules) {
    var page = el("div", "fs-page");
    if (withRules) page.appendChild(rules(k));
    var no = el("div", "fs-page-no");
    no.textContent = "Page " + (k + 1) + " of " + total;
    page.appendChild(no);
    return page;
  }

  // One sheet of paper behind each column the text fills.
  var pages = 1;
  var extra = 0;  // blank pages added after the text
  function paginate() {
    var written = pageCount();
    var total = pages = written + extra;
    pageInfo.textContent = total + (total === 1 ? " page" : " pages") +
      (extra ? " (" + extra + " blank at the end)" : "");
    removePage.disabled = extra === 0;
    var key = JSON.stringify([total, layout]);
    if (sheets.dataset.key !== key) {
      sheets.textContent = "";
      for (var k = 0; k < total; k++) {
        var page = pageNode(k, total, true);
        page.style.left = sheetX(k) + "mm";
        page.style.top = (layout.cTop - margins(k).top) + "mm";
        sheets.appendChild(page);
      }
      sheets.dataset.key = key;
    }
    strip.style.width = (sheetX(total - 1) + layout.w) + "mm";
    strip.style.height = (layout.h + Math.abs(layout.margins[0].top - layout.margins[1].top)) + "mm";
    renderDrawn();
    var choice = fields.zoom.value;
    var zoom = choice === "fit"
      ? Math.min(1, (desk.clientWidth - 8) / ((2 * layout.w + SPACE) * PX_PER_MM))
      : parseFloat(choice);
    scaler.style.zoom = zoom;
  }

  var pending = false;
  function later() {
    if (pending) return;
    pending = true;
    requestAnimationFrame(function () {
      pending = false;
      paginate();
    });
  }

  // For printing: each page shows its own column of a copy of the text.
  // The count comes from the screen layout: once printing has started the
  // sheet on screen is hidden and can't be measured.
  function buildPrint() {
    if (content.getClientRects().length) apply();
    var total = pages;
    printPages.textContent = "";
    for (var k = 0; k < total; k++) {
      var page = pageNode(k, total, fields.printLines.checked);
      var copy = content.cloneNode(true);
      copy.removeAttribute("id");
      copy.removeAttribute("contenteditable");
      // Its own column lands inside this page's margins.
      copy.style.left = (margins(k).left - k * layout.pitch) + "mm";
      copy.style.top = "calc(" + margins(k).top + "mm + var(--fs-shift))";
      page.appendChild(copy);
      drawn.forEach(function (d) { if (d.p === k) page.appendChild(lineNode(d, 0, 0)); });
      printPages.appendChild(page);
    }
  }

  function showNote(text) {
    note.hidden = !text;
    note.textContent = text || "";
  }

  // Whether the browser can draw ML-TT Karthika: the site's copy of the
  // font, or the computer's own.
  function karthikaAvailable() {
    if (!document.fonts || !document.fonts.load) return Promise.resolve(false);
    return document.fonts.load('16px "ML-TT Karthika"', "A").then(function (faces) {
      return faces.length > 0;
    }, function () { return false; });
  }

  function chooseFont() {
    if (busy) return;
    var want = fields.font.value === "karthika" ? "legacy" : "unicode";
    busy = true;
    fields.font.disabled = true;
    setMode(want).then(function () {
      showNote(want === "legacy"
        ? "The sheet is in ML-TT Karthika: type corrections the way you would in PageMaker with this font."
        : "");
    }, function () {
      showNote("Couldn't switch the font just now. Please try again.");
      fields.font.value = mode === "legacy" ? "karthika" : "serif";
    }).then(function () {
      busy = false;
      fields.font.disabled = false;
      apply();
    });
  }

  // --- saving -------------------------------------------------------------------

  var SETTING_KEYS = ["top", "bottom", "left", "right", "topEven", "bottomEven", "leftEven", "lines", "gap",
                      "lift", "size", "font", "paper", "zoom", "printLines"];

  function collectSettings() {
    var out = { mode: mode, extra: extra, drawn: drawn.map(function (d) {
      return { p: d.p, h: d.h, x: d.x, y: d.y, len: d.len };
    }) };
    SETTING_KEYS.forEach(function (k) {
      out[k] = fields[k].type === "checkbox" ? fields[k].checked : fields[k].value;
    });
    return out;
  }

  // Saved HTML comes back through an inert document and keeps only the
  // elements and attributes the sheet itself makes.
  var ALLOWED = { P: 1, SPAN: 1, B: 1, I: 1, BR: 1, TABLE: 1, COLGROUP: 1, COL: 1, TBODY: 1, TR: 1, TD: 1, DIV: 1 };
  var ALLOWED_ATTRS = { "class": 1, style: 1, colspan: 1, rowspan: 1 };
  function clean(parent) {
    Array.prototype.slice.call(parent.childNodes).forEach(function (node) {
      if (node.nodeType === 3) return;
      if (node.nodeType !== 1) {
        node.remove();
        return;
      }
      clean(node);
      if (!ALLOWED[node.tagName]) {
        if (/^(SCRIPT|STYLE|IFRAME|OBJECT|EMBED|TEMPLATE)$/.test(node.tagName)) node.remove();
        else node.replaceWith.apply(node, Array.prototype.slice.call(node.childNodes));
        return;
      }
      Array.prototype.slice.call(node.attributes).forEach(function (a) {
        if (!ALLOWED_ATTRS[a.name.toLowerCase()] || /url\s*\(|expression/i.test(a.value)) node.removeAttribute(a.name);
      });
    });
  }

  function restore(saved) {
    if (!saved || !saved.content) return false;
    var body = new DOMParser().parseFromString("<body>" + saved.content + "</body>", "text/html").body;
    clean(body);
    content.textContent = "";
    while (body.firstChild) content.appendChild(document.adoptNode(body.firstChild));
    var st = saved.settings || {};
    SETTING_KEYS.forEach(function (k) {
      if (st[k] == null) return;
      if (fields[k].type === "checkbox") fields[k].checked = !!st[k];
      else fields[k].value = st[k];
    });
    if (!fields.font.value) fields.font.value = "serif";
    mode = st.mode === "legacy" ? "legacy" : "unicode";
    extra = Math.max(0, parseInt(st.extra, 10) || 0);
    drawn = (Array.isArray(st.drawn) ? st.drawn : []).filter(function (d) {
      return d && [d.p, d.x, d.y, d.len].every(function (v) { return typeof v === "number" && isFinite(v); }) &&
        d.p >= 0 && d.len > 0;
    }).map(function (d) { return { p: Math.floor(d.p), h: !!d.h, x: d.x, y: d.y, len: d.len }; });
    return true;
  }

  var saveButton = document.getElementById("fsSave");
  var saveState = document.getElementById("fsSaveState");
  var dirty = false;
  var saving = false;

  function markDirty() {
    if (!window.FS_SAVE_URL) return;
    dirty = true;
    if (saveState) saveState.textContent = "Unsaved changes";
  }

  function save() {
    if (!window.FS_SAVE_URL || saving || busy) return;
    saving = true;
    dirty = false;
    if (saveState) saveState.textContent = "Saving…";
    if (saveButton) saveButton.disabled = true;
    fetch(window.FS_SAVE_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-CSRFToken": window.FS_CSRF },
      body: JSON.stringify({ content: content.innerHTML, settings: collectSettings(), pages: pages }),
      credentials: "same-origin"
    }).then(function (r) {
      if (!r.ok) throw new Error("save failed");
      if (saveState) {
        saveState.textContent = "Saved " + new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
      }
    }).catch(function () {
      dirty = true;
      if (saveState) saveState.textContent = "Couldn't save just now; it will try again";
    }).then(function () {
      saving = false;
      if (saveButton) saveButton.disabled = false;
    });
  }

  // --- drawn lines ----------------------------------------------------------------
  //
  // A drawn line is across (h) or down, from (x, y) on its page for len mm.
  // On screen each is a strip a few mm wide that's easy to catch with the
  // mouse, with the line itself as a border down its middle (borders print,
  // where backgrounds may not). Ends snap to the ruled lines, the margins
  // and the other drawn lines' ends when they come close.
  var layer = document.getElementById("fsDrawn");
  var drawButton = document.getElementById("fsDraw");
  var deleteButton = document.getElementById("fsDeleteLine");
  var tableButton = document.getElementById("fsTableLines");
  var drawing = false;
  var SNAP = 1.5;  // mm

  function pageTop(k) { return layout.cTop - margins(k).top; }

  function lineNode(d, left, top) {
    var node = el("div", "fs-dl " + (d.h ? "fs-dl-h" : "fs-dl-v"));
    node.style.left = (left + d.x) + "mm";
    node.style.top = (top + d.y) + "mm";
    node.style[d.h ? "width" : "height"] = d.len + "mm";
    return node;
  }

  function renderDrawn() {
    if (!layer || !layout) return;
    layer.textContent = "";
    drawn.forEach(function (d, i) {
      if (d.p >= pages) return;
      var node = lineNode(d, sheetX(d.p), pageTop(d.p));
      node.dataset.i = i;
      if (i === selected) {
        node.classList.add("fs-dl-sel");
        node.appendChild(el("span", "fs-dh fs-dh-start")).dataset.end = "start";
        node.appendChild(el("span", "fs-dh fs-dh-end")).dataset.end = "end";
      }
      layer.appendChild(node);
    });
    if (deleteButton) deleteButton.hidden = !drawn[selected];
    if (tableButton) tableButton.hidden = !content.querySelector("td.fs-vline");
  }

  function select(i) {
    selected = i;
    renderDrawn();
  }

  // Where the pointer is, in mm from the strip's corner, whatever the zoom.
  function pointer(e) {
    var box = strip.getBoundingClientRect();
    var scale = parseFloat(strip.style.width) / box.width;
    return { x: (e.clientX - box.left) * scale, y: (e.clientY - box.top) * scale };
  }

  function pageAt(pt) {
    for (var k = 0; k < pages; k++) {
      var x = pt.x - sheetX(k), y = pt.y - pageTop(k);
      if (x >= 0 && x <= layout.w && y >= 0 && y <= layout.h) return k;
    }
    return -1;
  }

  // Places a line's ends may snap to on page k, leaving out line `skip`.
  function stops(k, skip, across) {
    var m = margins(k), out = [];
    if (across) {
      out.push(m.left, m.left + layout.colW);
    } else {
      for (var n = 0; n <= layout.lines; n++) out.push(m.top + n * layout.gap);
    }
    drawn.forEach(function (d, i) {
      if (i === skip || d.p !== k) return;
      if (across) out.push(d.x, d.h ? d.x + d.len : d.x);
      else out.push(d.y, d.h ? d.y : d.y + d.len);
    });
    return out;
  }

  function snap(v, list) {
    var best = round(v, 0.5), dist = SNAP;
    list.forEach(function (s) {
      if (Math.abs(s - v) < dist) {
        dist = Math.abs(s - v);
        best = s;
      }
    });
    return best;
  }

  function clamp(v, lo, hi) { return Math.min(hi, Math.max(lo, v)); }

  // Keeps a line on its page, at least 1 mm long.
  function fit(d) {
    d.len = clamp(d.len, 1, d.h ? layout.w : layout.h);
    d.x = clamp(d.x, 0, layout.w - (d.h ? d.len : 0));
    d.y = clamp(d.y, 0, layout.h - (d.h ? 0 : d.len));
  }

  var drag = null;

  function startDrag(e) {
    if (window.FS_VIEW || !layout) return;
    var target = e.target.closest(".fs-dl");
    var pt = pointer(e);
    if (target) {
      var i = +target.dataset.i;
      var d = drawn[i];
      drag = { kind: e.target.dataset.end || "move", i: i, from: pt,
               start: { x: d.x, y: d.y, len: d.len } };
      select(i);
    } else if (drawing) {
      var k = pageAt(pt);
      if (k < 0) return;
      var x = pt.x - sheetX(k), y = pt.y - pageTop(k);
      drawn.push({ p: k, h: true, x: x, y: y, len: 0 });
      drag = { kind: "new", i: drawn.length - 1, from: pt, origin: { x: x, y: y } };
      select(drag.i);
    } else {
      return;
    }
    // The cursor leaves the text while a line is worked on.
    if (document.activeElement === content) content.blur();
    e.preventDefault();
    layer.classList.add("fs-dragging");
    layer.setPointerCapture(e.pointerId);
  }

  function moveDrag(e) {
    if (!drag) return;
    var d = drawn[drag.i];
    var pt = pointer(e);
    var dx = pt.x - drag.from.x, dy = pt.y - drag.from.y;
    var along = d.h ? stops(d.p, drag.i, true) : stops(d.p, drag.i, false);
    var across = d.h ? stops(d.p, drag.i, false) : stops(d.p, drag.i, true);
    if (drag.kind === "new") {
      // Across or down, whichever way the mouse has gone further.
      var o = drag.origin;
      d.h = Math.abs(dx) >= Math.abs(dy);
      along = stops(d.p, drag.i, d.h);
      across = stops(d.p, drag.i, !d.h);
      var a = snap(d.h ? o.x : o.y, along), b = snap((d.h ? o.x + dx : o.y + dy), along);
      d.len = Math.abs(b - a);
      if (d.h) { d.x = Math.min(a, b); d.y = snap(o.y, across); }
      else { d.y = Math.min(a, b); d.x = snap(o.x, across); }
    } else if (drag.kind === "move") {
      var s = drag.start;
      if (d.h) {
        d.x = snap(s.x + dx, along);
        d.y = snap(s.y + dy, across);
      } else {
        d.x = snap(s.x + dx, across);
        d.y = snap(s.y + dy, along);
      }
    } else {
      // Stretching from one end: the other end stays where it was.
      var st = drag.start, pos = d.h ? st.x : st.y, end = pos + st.len;
      var to = snap((d.h ? st.x : st.y) + (drag.kind === "start" ? 0 : st.len) + (d.h ? dx : dy), along);
      if (drag.kind === "start") { pos = Math.min(to, end - 1); }
      else { end = Math.max(to, pos + 1); }
      d.len = end - pos;
      if (d.h) d.x = pos; else d.y = pos;
    }
    if (drag.kind !== "new" || d.len) fit(d);
    renderDrawn();
  }

  function endDrag() {
    if (!drag) return;
    var d = drawn[drag.i];
    // A click while drawing, without a drag, makes no line.
    if (drag.kind === "new" && d.len < 2) {
      drawn.splice(drag.i, 1);
      selected = -1;
    }
    var changed = drag.kind !== "new" || d.len >= 2;
    drag = null;
    layer.classList.remove("fs-dragging");
    renderDrawn();
    if (changed) markDirty();
  }

  function deleteSelected() {
    if (!drawn[selected]) return;
    drawn.splice(selected, 1);
    selected = -1;
    renderDrawn();
    markDirty();
  }

  function setDrawing(on) {
    drawing = on;
    layer.classList.toggle("fs-drawing", on);
    if (drawButton) {
      drawButton.classList.toggle("active", on);
      drawButton.setAttribute("aria-pressed", String(on));
    }
  }

  // The lines between a table's columns are borders of its cells, so they
  // follow the text. This draws a line where each one is now, merging a
  // column's cells into one line per page, and takes the borders away:
  // from then on they're lines like any other, to move and stretch.
  var HALF_BORDER = 0.75 * MM_PER_PT / 2;
  function tableLinesToDrawn() {
    var cells = content.querySelectorAll("td.fs-vline");
    if (!cells.length) return;
    var box = strip.getBoundingClientRect();
    var scale = parseFloat(strip.style.width) / box.width;
    var found = [];
    cells.forEach(function (td) {
      // A cell split between two pages has a box on each.
      Array.prototype.forEach.call(td.getClientRects(), function (r) {
        if (!r.height) return;
        var pt = { x: (r.left - box.left) * scale + HALF_BORDER, y: (r.top - box.top) * scale };
        var k = pageAt(pt);
        if (k < 0) return;
        found.push({ p: k, h: false, x: round(pt.x - sheetX(k), 0.1), y: pt.y - pageTop(k), len: r.height * scale });
      });
      td.classList.remove("fs-vline");
    });
    found.sort(function (a, b) { return a.p - b.p || a.x - b.x || a.y - b.y; });
    var merged = [];
    found.forEach(function (d) {
      var last = merged[merged.length - 1];
      if (last && last.p === d.p && Math.abs(last.x - d.x) < 0.3 && d.y <= last.y + last.len + 0.5) {
        last.len = Math.max(last.len, d.y + d.len - last.y);
      } else {
        merged.push(d);
      }
    });
    merged.forEach(function (d) {
      d.y = round(d.y, 0.1);
      d.len = round(d.len, 0.1);
      drawn.push(d);
    });
    edited = true;
    markDirty();
    selected = -1;
    paginate();
  }

  if (layer && !window.FS_VIEW) {
    layer.addEventListener("pointerdown", startDrag);
    layer.addEventListener("pointermove", moveDrag);
    layer.addEventListener("pointerup", endDrag);
    layer.addEventListener("pointercancel", endDrag);
    if (drawButton) drawButton.addEventListener("click", function () { setDrawing(!drawing); });
    if (tableButton) tableButton.addEventListener("click", tableLinesToDrawn);
    if (deleteButton) {
      deleteButton.addEventListener("mousedown", function (e) { e.preventDefault(); });
      deleteButton.addEventListener("click", deleteSelected);
    }
    // Clicking anywhere else lets go of the picked line. (This runs before
    // the line under the pointer is drawn again, while it's still there.)
    document.addEventListener("pointerdown", function (e) {
      if (selected < 0 || layer.contains(e.target) || (deleteButton && deleteButton.contains(e.target))) return;
      select(-1);
    }, true);
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape") {
        if (selected >= 0) select(-1);
        else if (drawing) setDrawing(false);
        return;
      }
      var d = drawn[selected];
      if (!d || document.activeElement === content) return;
      if (e.key === "Delete" || e.key === "Backspace") {
        e.preventDefault();
        deleteSelected();
        return;
      }
      var step = e.shiftKey ? 5 : 0.5;
      var move = { ArrowLeft: [-step, 0], ArrowRight: [step, 0], ArrowUp: [0, -step], ArrowDown: [0, step] }[e.key];
      if (!move) return;
      e.preventDefault();
      d.x += move[0];
      d.y += move[1];
      fit(d);
      renderDrawn();
      markDirty();
    });
  }

  // --- start --------------------------------------------------------------------

  function mm(pt) { return Math.round(pt * MM_PER_PT); }
  fields.top.value = mm(doc.margins[0]);
  fields.right.value = mm(doc.margins[1]);
  fields.bottom.value = mm(doc.margins[2]);
  fields.left.value = mm(doc.margins[3]);
  fields.topEven.value = fields.top.value;
  fields.leftEven.value = fields.left.value;
  fields.size.value = doc.size;
  fields.gap.value = Math.max(5, round(doc.size * 1.6 * MM_PER_PT, 0.5));
  fields.lift.value = 1.5;
  fields.font.value = "serif";
  linesFromBottom();
  bottomFromLines();
  render();
  var savedEl = document.getElementById("fs-saved");
  var restored = restore(savedEl ? JSON.parse(savedEl.textContent) : null);
  apply();

  // While a number is typed, only the other fields that follow from it
  // change; it settles when the field is left.
  function otherBottom(even) {
    var keep = (even ? fields.bottomEven : fields.bottom).value;
    bottomFromLines();
    (even ? fields.bottomEven : fields.bottom).value = keep;
  }
  fields.lines.addEventListener("input", function () { bottomFromLines(); apply(); });
  [["top", false], ["bottom", false], ["topEven", true], ["bottomEven", true], ["gap", false], ["paper", false]]
    .forEach(function (pair) {
      fields[pair[0]].addEventListener("input", function () {
        linesFromBottom(pair[1]);
        if (pair[0].indexOf("bottom") === 0) otherBottom(pair[1]);
        else bottomFromLines();
        apply();
      });
    });
  ["left", "right", "leftEven", "lift", "size", "zoom"].forEach(function (k) {
    fields[k].addEventListener("input", apply);
    fields[k].addEventListener("change", apply);
  });
  ["top", "bottom", "topEven", "bottomEven", "gap", "lines"].forEach(function (k) {
    fields[k].addEventListener("change", function () { bottomFromLines(); apply(); });
  });

  // The settings fold away to leave room for the pages.
  var fold = document.getElementById("fsFold");
  var settingsPanel = document.getElementById("fsSettings");
  function setFolded(folded) {
    settingsPanel.hidden = folded;
    fold.setAttribute("aria-expanded", String(!folded));
    fold.querySelector("[data-fold-icon]").className = "bi bi-chevron-" + (folded ? "down" : "up");
    try { localStorage.setItem("fsSettingsFolded", folded ? "1" : ""); } catch (e) { /* private mode */ }
    later();
  }
  if (fold) {
    fold.addEventListener("click", function () { setFolded(!settingsPanel.hidden); });
    try { if (localStorage.getItem("fsSettingsFolded")) setFolded(true); } catch (e) { /* private mode */ }
  }
  fields.font.addEventListener("change", chooseFont);

  // --- alignment ------------------------------------------------------------------
  //
  // The buttons align the paragraphs the cursor or selection is in (or a
  // table cell's). Enter starts a new paragraph, so a line such as a heading
  // can be set apart and centred.
  try { document.execCommand("defaultParagraphSeparator", false, "p"); } catch (e) { /* older browsers */ }
  var alignButtons = document.querySelectorAll(".fs-align [data-align]");
  var lastRange = null;

  function blocksInSelection(range) {
    var out = [];
    content.querySelectorAll("p").forEach(function (p) {
      if (range.intersectsNode(p)) out.push(p);
    });
    return out;
  }

  function showAlign() {
    var sel = window.getSelection();
    var align = null;
    if (sel.rangeCount && content.contains(sel.anchorNode)) {
      lastRange = sel.getRangeAt(0).cloneRange();
      var node = sel.anchorNode.nodeType === 1 ? sel.anchorNode : sel.anchorNode.parentNode;
      var block = node.closest("p, td");
      if (block && content.contains(block)) align = getComputedStyle(block).textAlign;
      if (align === "start") align = "left";
    }
    alignButtons.forEach(function (b) { b.classList.toggle("active", b.dataset.align === align); });
  }

  document.addEventListener("selectionchange", showAlign);
  alignButtons.forEach(function (button) {
    // Keep the cursor in the sheet while clicking.
    button.addEventListener("mousedown", function (e) { e.preventDefault(); });
    button.addEventListener("click", function () {
      if (!lastRange) return;
      var blocks = blocksInSelection(lastRange);
      if (!blocks.length) {
        var node = lastRange.startContainer;
        var block = (node.nodeType === 1 ? node : node.parentNode).closest("p, td");
        if (block && content.contains(block)) blocks = [block];
      }
      blocks.forEach(function (b) { b.style.textAlign = button.dataset.align; });
      edited = true;
      markDirty();
      showAlign();
      later();
    });
  });
  content.addEventListener("input", function () {
    edited = true;
    markDirty();
    later();
  });
  Object.keys(fields).forEach(function (k) {
    fields[k].addEventListener("change", markDirty);
    fields[k].addEventListener("input", markDirty);
  });
  // Pasted text comes in plain, in the sheet's own font and size.
  content.addEventListener("paste", function (e) {
    var text = (e.clipboardData || window.clipboardData).getData("text/plain");
    if (text == null) return;
    e.preventDefault();
    document.execCommand("insertText", false, text);
  });
  window.addEventListener("resize", later);
  window.addEventListener("beforeprint", buildPrint);
  window.addEventListener("afterprint", function () { printPages.textContent = ""; });
  window.addEventListener("beforeunload", function (e) {
    if (!dirty) return;
    e.preventDefault();
    e.returnValue = "";
  });

  var karthikaOption = fields.font.querySelector('option[value="karthika"]');
  karthikaAvailable().then(function (ok) {
    if (ok) {
      // Deeds are typed in it, so it's where the sheet starts.
      if (!edited && !restored) {
        fields.font.value = "karthika";
        chooseFont();
      }
    } else {
      karthikaOption.disabled = true;
      karthikaOption.textContent = "ML-TT Karthika (not installed yet)";
    }
  });
  if (document.fonts && document.fonts.ready) document.fonts.ready.then(apply);

  // Blank pages at the end: added for writing more by hand, and taken
  // away again. Only the blank ones can go; the text's own pages stay.
  addPage.addEventListener("click", function () { extra += 1; markDirty(); paginate(); });
  removePage.addEventListener("click", function () {
    if (extra > 0) {
      extra -= 1;
      // Lines drawn on the page that goes, go with it.
      drawn = drawn.filter(function (d) { return d.p < pages - 1; });
      selected = -1;
    }
    markDirty();
    paginate();
  });

  if (saveButton) saveButton.addEventListener("click", save);
  document.addEventListener("keydown", function (e) {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s" && window.FS_SAVE_URL) {
      e.preventDefault();
      save();
    }
  });
  // Saves itself every minute while there's something new to save.
  if (window.FS_SAVE_URL) setInterval(function () { if (dirty) save(); }, 60000);

  document.getElementById("fsPrint").addEventListener("click", function () {
    buildPrint();
    window.print();
  });
  var reset = document.getElementById("fsReset");
  if (reset) reset.addEventListener("click", function () {
    if ((edited || restored || drawn.length) && !window.confirm("Throw away all your corrections and start again from the file?")) return;
    var font = fields.font.value;
    render();
    drawn = [];
    selected = -1;
    markDirty();
    if (font === "karthika") {
      fields.font.value = "karthika";
      chooseFont();
    }
    apply();
  });
  var copy = document.getElementById("fsCopy");
  copy.addEventListener("click", function () {
    var text = content.innerText;
    if (!navigator.clipboard || !navigator.clipboard.writeText) return;
    navigator.clipboard.writeText(text).then(function () {
      var old = copy.innerHTML;
      copy.innerHTML = '<i class="bi bi-check2"></i> Copied';
      setTimeout(function () { copy.innerHTML = old; }, 1500);
    }, function () {});
  });
})();
