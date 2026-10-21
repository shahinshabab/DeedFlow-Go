// File reader: draws the pages of a file read on the server (see
// filereader/model.py for the shape of the data). Every page is laid out at
// its real size in CSS points, with each text frame at its original
// position, and then scaled to the zoom level. Text that runs through a
// chain of linked frames is flowed from one frame into the next here, the
// way the publishing program does, since the files only store the text and
// the frames, not where the lines break.
(function () {
  "use strict";

  var dataEl = document.getElementById("fr-doc");
  var host = document.getElementById("frPages");
  if (!dataEl || !host) return;
  var doc = JSON.parse(dataEl.textContent);
  var PAGE_NUMBER = "\ue000";
  var PX_PER_PT = 96 / 72;
  var FALLBACK_FONTS = '"Noto Serif Malayalam", "Noto Sans Malayalam", "Times New Roman", serif';
  var zoomEl = document.getElementById("frZoom");

  function el(tag, cls, style) {
    var node = document.createElement(tag);
    if (cls) node.className = cls;
    if (style) Object.assign(node.style, style);
    return node;
  }

  function pt(v) { return (v || 0) + "pt"; }
  // Type size, times the story's fit factor (see fitStory).
  function typeSize(v) { return "calc(" + (v || 0) + "pt * var(--fr-fit, 1))"; }

  function fontFamily(span) {
    // A legacy font's glyph codes have been converted to Unicode, so it's
    // drawn in a Unicode Malayalam font; other fonts are asked for by name.
    // PDF font names carry a "ABCDEF+" subset prefix.
    var name = span.legacy ? "" : (span.font || "").replace(/^[A-Z]{6}\+/, "").replace(/["\\]/g, "");
    return (name ? '"' + name + '", ' : "") + FALLBACK_FONTS;
  }

  // --- text blocks --------------------------------------------------------

  function renderSpans(spans, pageNumber) {
    var frag = document.createDocumentFragment();
    spans.forEach(function (s) {
      var node = el("span", null, {
        fontFamily: fontFamily(s),
        fontSize: typeSize(s.size),
        fontWeight: s.bold ? "700" : "400",
        fontStyle: s.italic ? "italic" : "normal",
        textDecoration: s.underline ? "underline" : "none",
        color: s.color || "#000"
      });
      var text = s.t.split(PAGE_NUMBER).join(String(pageNumber || ""));
      node.textContent = text || "\u200b";
      frag.appendChild(node);
    });
    return frag;
  }

  function renderParagraph(b, pageNumber) {
    var p = el("p", "fr-p", {
      textAlign: b.align || "left",
      textIndent: pt(b.indent),
      margin: [pt(b.before), pt(b.right), pt(b.after), pt(b.left)].join(" "),
      lineHeight: b.leading ? pt(b.leading) : "1.2"
    });
    // The paragraph's own font sets the line's baseline (its "strut"), so
    // give it the biggest run's font; otherwise a mixed baseline makes each
    // line taller than its leading.
    var biggest = b.spans.reduce(function (a, s) { return !a || s.size > a.size ? s : a; }, null);
    if (biggest) {
      p.style.fontSize = typeSize(biggest.size);
      p.style.fontFamily = fontFamily(biggest);
    }
    if (b.continued) p.style.textIndent = "0";
    if (b.split && b.align === "justify") p.style.textAlignLast = "justify";
    p.appendChild(renderSpans(b.spans, pageNumber));
    return p;
  }

  function renderTable(b, pageNumber) {
    var table = el("table", "fr-table");
    var total = 0;
    var colgroup = el("colgroup");
    (b.cols || []).forEach(function (w) {
      total += w;
      colgroup.appendChild(el("col", null, { width: pt(w) }));
    });
    table.appendChild(colgroup);
    if (total) table.style.width = pt(total);
    var tbody = el("tbody");
    b.rows.forEach(function (row) {
      var tr = el("tr");
      row.cells.forEach(function (cell) {
        var td = el("td");
        if (cell.colspan > 1) td.colSpan = cell.colspan;
        if (cell.rowspan > 1) td.rowSpan = cell.rowspan;
        var holder = td;
        var turn = ((cell.rotate || 0) % 360 + 360) % 360;
        if (turn === 90 || turn === 270) {
          // Text set on its side: top to bottom, or bottom to top.
          holder = el("div", "fr-cell-turned" + (turn === 270 ? " fr-cell-up" : ""));
          td.appendChild(holder);
        }
        cell.blocks.forEach(function (inner) { holder.appendChild(renderBlock(inner, pageNumber)); });
        tr.appendChild(td);
      });
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    return table;
  }

  function renderBlock(b, pageNumber) {
    return b.kind === "table" ? renderTable(b, pageNumber) : renderParagraph(b, pageNumber);
  }

  // --- flowing a story through linked frames ------------------------------

  function fits(flow) { return flow.scrollHeight <= flow.clientHeight + 0.5; }

  function tokens(b) {
    // Words with their trailing spaces, each remembering its span.
    var out = [];
    b.spans.forEach(function (s, i) {
      (s.t.match(/\S+\s*|\s+/g) || [""]).forEach(function (t) { out.push({ span: i, t: t }); });
    });
    return out;
  }

  function fromTokens(b, toks, extra) {
    var spans = [];
    toks.forEach(function (tok) {
      var last = spans[spans.length - 1];
      if (last && last._i === tok.span) last.t += tok.t;
      else spans.push(Object.assign({}, b.spans[tok.span], { t: tok.t, _i: tok.span }));
    });
    return Object.assign({}, b, { spans: spans }, extra);
  }

  // Puts as much of block `b` into `flow` as fits. Returns what's left over
  // (or `b` itself when none of it fits).
  function splitInto(flow, b, pageNumber) {
    var lo = 0, hi, make;
    if (b.kind === "table") {
      hi = b.rows.length - 1;
      make = function (k, rest) {
        return Object.assign({}, b, { rows: rest ? b.rows.slice(k) : b.rows.slice(0, k) });
      };
    } else {
      var toks = tokens(b);
      hi = toks.length - 1;
      make = function (k, rest) {
        return rest ? fromTokens(b, toks.slice(k), { continued: true, before: 0 })
                    : fromTokens(b, toks.slice(0, k), { split: true, after: 0 });
      };
    }
    // Largest k in [0, hi] whose first k pieces still fit.
    var node = null;
    while (lo < hi) {
      var mid = Math.ceil((lo + hi) / 2);
      var trial = renderBlock(make(mid, false), pageNumber);
      flow.appendChild(trial);
      var ok = fits(flow);
      flow.removeChild(trial);
      if (ok) lo = mid; else hi = mid - 1;
    }
    if (lo === 0) return b;
    node = renderBlock(make(lo, false), pageNumber);
    flow.appendChild(node);
    return make(lo, true);
  }

  function flowStory(frames, blocks) {
    var queue = blocks.slice();
    frames.forEach(function (f, i) {
      var last = i === frames.length - 1;
      while (queue.length) {
        var node = renderBlock(queue[0], f.pageNumber);
        f.flow.appendChild(node);
        if (last || fits(f.flow)) { queue.shift(); continue; }
        f.flow.removeChild(node);
        var rest = splitInto(f.flow, queue[0], f.pageNumber);
        if (rest === queue[0] && !f.flow.childNodes.length) {
          // Nothing fits even in an empty frame (a huge word or row): put it
          // here anyway rather than lose it.
          f.flow.appendChild(renderBlock(queue.shift(), f.pageNumber));
          continue;
        }
        queue[0] = rest;
        break;
      }
    });
  }

  // --- pages ----------------------------------------------------------------

  var pages = [];

  function buildPage(page, index) {
    var wrap = el("section", "fr-page-wrap");
    wrap.setAttribute("aria-label", "Page " + (index + 1));
    var label = el("div", "fr-page-label");
    label.appendChild(document.createTextNode("Page " + (index + 1) + " of " + doc.pages.length));
    var copy = el("button");
    copy.type = "button";
    copy.innerHTML = '<i class="bi bi-clipboard"></i> Copy text';
    label.appendChild(copy);
    var scaler = el("div", "fr-scaler");
    var sheet = el("div", "fr-page", { width: pt(page.width), height: pt(page.height) });
    var frames = [];

    page.items.forEach(function (item) {
      if (item.type === "path") {
        var svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
        svg.setAttribute("viewBox", "0 0 " + page.width + " " + page.height);
        var path = document.createElementNS("http://www.w3.org/2000/svg", "path");
        path.setAttribute("d", item.d);
        path.setAttribute("fill", item.fill || "none");
        path.setAttribute("stroke", item.stroke || "none");
        path.setAttribute("stroke-width", item.sw || 1);
        svg.appendChild(path);
        sheet.appendChild(svg);
        return;
      }
      var box = el("div", item.type === "image" ? "fr-image" : "fr-frame", {
        left: pt(item.x), top: pt(item.y), width: pt(item.w), height: pt(item.h)
      });
      if (item.rotate) {
        box.style.transformOrigin = item.origin === "center" ? "center" : "top left";
        box.style.transform = "rotate(" + item.rotate + "deg)";
      }
      if (item.type === "image") {
        if (item.src) {
          var img = el("img");
          img.src = item.src;
          img.alt = "";
          box.classList.add("has-img");
          box.appendChild(img);
        } else {
          box.textContent = item.label || "Image";
        }
        sheet.appendChild(box);
        return;
      }
      var inset = item.inset || [0, 0, 0, 0];
      box.style.padding = [pt(inset[0]), pt(inset[3]), pt(inset[2]), pt(inset[1])].join(" ");
      if (item.valign && item.valign !== "top") box.classList.add("fr-valign-" + item.valign);
      if (item.nowrap) box.classList.add("fr-nowrap");
      var flow = el("div", "fr-flow");
      box.appendChild(flow);
      sheet.appendChild(box);
      frames.push({ item: item, node: box, flow: flow, pageNumber: index + 1 });
    });

    scaler.appendChild(sheet);
    wrap.appendChild(label);
    wrap.appendChild(scaler);
    host.appendChild(wrap);
    var entry = { page: page, scaler: scaler, sheet: sheet, frames: frames };
    copy.addEventListener("click", function () { copyText(pageText(entry), copy); });
    pages.push(entry);
  }

  // Lays a story out across its frames with its type at `fit` of its size
  // (the leading stays), and returns how far the last frame overflows (px).
  function layout(frames, blocks, fit) {
    frames.forEach(function (f) {
      f.flow.textContent = "";
      if (fit < 1) f.flow.style.setProperty("--fr-fit", fit);
      else f.flow.style.removeProperty("--fr-fit");
    });
    flowStory(frames, blocks);
    var last = frames[frames.length - 1].flow;
    return last.scrollHeight - last.clientHeight;
  }

  // The legacy fonts the files were set in (ML-TT and friends) draw their
  // letters much smaller for the same point size than any Unicode Malayalam
  // font a browser has, so the same words take more lines here and a story
  // can run past the end of its frames. When it does, its type is made
  // smaller, keeping the leading, just enough that it fits again, the way
  // it did in the original.
  var MIN_FIT = 0.6;

  function fitStory(frames, blocks) {
    var autoHeight = frames.some(function (f) { return !(f.item.h > 0); });
    if (autoHeight) {
      frames.forEach(function (f) { f.node.style.height = "auto"; });
      layout(frames, blocks, 1);
      return;
    }
    if (layout(frames, blocks, 1) <= 2) return;
    var lo = MIN_FIT, hi = 1;
    if (layout(frames, blocks, lo) > 2) return;
    for (var i = 0; i < 7; i++) {
      var mid = (lo + hi) / 2;
      if (layout(frames, blocks, mid) <= 2) lo = mid; else hi = mid;
    }
    layout(frames, blocks, lo);
  }

  function fillFrames() {
    // Frames that share a story, in page order, are one linked chain. Master
    // page frames repeat on every page, so each shows its whole story.
    var groups = {};
    var order = [];
    pages.forEach(function (p) {
      p.frames.forEach(function (f) {
        var blocks = doc.stories[f.item.story] || [];
        if (f.item.nowrap) {
          blocks.forEach(function (b) { f.flow.appendChild(renderBlock(b, f.pageNumber)); });
          return;
        }
        var key = f.item.repeat ? "repeat:" + order.length : f.item.story;
        if (!groups[key]) { groups[key] = { story: f.item.story, frames: [] }; order.push(key); }
        groups[key].frames.push(f);
      });
    });
    order.forEach(function (key) {
      var g = groups[key];
      fitStory(g.frames, doc.stories[g.story] || []);
    });
    // Single lines (PDF text) are squeezed or stretched to the width they
    // had in the original, since the stand-in font's letters differ in width.
    pages.forEach(function (p) {
      p.frames.forEach(function (f) {
        if (!f.item.nowrap || !f.item.w) return;
        var actual = f.node.scrollWidth / PX_PER_PT;
        if (actual > 0) {
          var sx = Math.max(0.5, Math.min(1.6, f.item.w / actual));
          f.node.style.transform = "scaleX(" + sx.toFixed(3) + ")";
        }
      });
    });
  }

  function applyZoom() {
    var choice = zoomEl ? zoomEl.value : "fit";
    var avail = host.clientWidth - 8;
    pages.forEach(function (p) {
      var w = p.page.width * PX_PER_PT;
      var h = p.page.height * PX_PER_PT;
      var scale = choice === "fit" ? Math.min(1.5, avail / w) : parseFloat(choice);
      p.sheet.style.transform = "scale(" + scale + ")";
      p.scaler.style.width = w * scale + "px";
      p.scaler.style.height = h * scale + "px";
    });
  }

  function pageText(p) {
    return p.frames.map(function (f) { return f.flow.innerText.replace(/\u200b/g, ""); })
      .filter(function (t) { return t.trim(); }).join("\n\n");
  }

  function copyText(text, button) {
    var done = function () {
      var old = button.innerHTML;
      button.innerHTML = '<i class="bi bi-check2"></i> Copied';
      setTimeout(function () { button.innerHTML = old; }, 1500);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, function () {});
    }
  }

  doc.pages.forEach(buildPage);
  applyZoom();
  // Text is fitted to its frames by measuring it, so the Malayalam web
  // fonts have to be in before that starts. Browsers only fetch a font once
  // text needs it, so ask for each face explicitly.
  var ready = Promise.resolve();
  if (document.fonts && document.fonts.load) {
    ready = Promise.all(["400", "700"].map(function (weight) {
      return document.fonts.load(weight + ' 16px "Noto Serif Malayalam"', "\u0d2e\u0d32");
    })).catch(function () {}).then(function () { return document.fonts.ready; });
  }
  ready.then(function () {
    fillFrames();
    host.dataset.ready = "true";
  });
  if (zoomEl) zoomEl.addEventListener("change", applyZoom);
  window.addEventListener("resize", function () { if (!zoomEl || zoomEl.value === "fit") applyZoom(); });
  var copyAll = document.getElementById("frCopyAll");
  if (copyAll) {
    copyAll.addEventListener("click", function () {
      copyText(pages.map(pageText).join("\n\n\f\n\n"), copyAll);
    });
  }
})();
