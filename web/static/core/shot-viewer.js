// In-page viewer for portal screenshots: clicking a thumbnail in any .shot-grid
// opens it full size on the same page, with previous/next buttons, arrow keys and
// a row of tiles along the bottom to jump to any screenshot.
// Ctrl/Cmd/middle-click still opens the image in a new tab as before.
(function () {
  var viewer, img, caption, counter, prevBtn, nextBtn, strip;
  var links = [];
  var index = 0;
  var lastFocus = null;

  function build() {
    viewer = document.createElement("div");
    viewer.className = "shot-viewer";
    viewer.hidden = true;
    viewer.setAttribute("role", "dialog");
    viewer.setAttribute("aria-modal", "true");
    viewer.setAttribute("aria-label", "Screenshot viewer");
    viewer.innerHTML =
      '<div class="shot-viewer-bar">' +
      '  <div class="shot-viewer-title"><span class="shot-viewer-caption"></span><span class="shot-viewer-count"></span></div>' +
      '  <div class="shot-viewer-actions">' +
      '    <a class="shot-viewer-btn shot-viewer-open" target="_blank" rel="noopener" title="Open in new tab"><i class="bi bi-box-arrow-up-right"></i></a>' +
      '    <button type="button" class="shot-viewer-btn shot-viewer-close" title="Close (Esc)" aria-label="Close"><i class="bi bi-x-lg"></i></button>' +
      '  </div>' +
      '</div>' +
      '<button type="button" class="shot-viewer-nav shot-viewer-prev" title="Previous (←)" aria-label="Previous screenshot"><i class="bi bi-chevron-left"></i></button>' +
      '<div class="shot-viewer-stage"><img alt=""></div>' +
      '<button type="button" class="shot-viewer-nav shot-viewer-next" title="Next (→)" aria-label="Next screenshot"><i class="bi bi-chevron-right"></i></button>' +
      '<div class="shot-viewer-strip" aria-label="All screenshots"></div>';
    document.body.appendChild(viewer);

    img = viewer.querySelector(".shot-viewer-stage img");
    caption = viewer.querySelector(".shot-viewer-caption");
    counter = viewer.querySelector(".shot-viewer-count");
    prevBtn = viewer.querySelector(".shot-viewer-prev");
    nextBtn = viewer.querySelector(".shot-viewer-next");
    strip = viewer.querySelector(".shot-viewer-strip");

    strip.addEventListener("click", function (e) {
      var tile = e.target.closest(".shot-viewer-tile");
      if (tile) show(Number(tile.dataset.index));
    });

    prevBtn.addEventListener("click", function () { show(index - 1); });
    nextBtn.addEventListener("click", function () { show(index + 1); });
    viewer.querySelector(".shot-viewer-close").addEventListener("click", close);
    // Clicking the dark area around the image closes the viewer.
    viewer.querySelector(".shot-viewer-stage").addEventListener("click", function (e) {
      if (e.target !== img) close();
    });
  }

  function labelFor(link) {
    var cell = link.closest(".shot-grid > *") || link;
    var cap = cell.querySelector(".shot-caption");
    var time = cell.querySelector(".shot-time");
    var text = (cap && cap.textContent.trim()) || (link.querySelector("img") || {}).alt || "Screenshot";
    return time && time.textContent.trim() ? text + " · " + time.textContent.trim() : text;
  }

  function show(i) {
    if (!links.length) return;
    index = Math.max(0, Math.min(links.length - 1, i));
    var link = links[index];
    img.src = link.href;
    img.alt = labelFor(link);
    caption.textContent = labelFor(link);
    counter.textContent = (index + 1) + " of " + links.length;
    viewer.querySelector(".shot-viewer-open").href = link.href;
    prevBtn.disabled = index === 0;
    nextBtn.disabled = index === links.length - 1;
    Array.prototype.forEach.call(strip.children, function (tile, i) {
      tile.classList.toggle("active", i === index);
      tile.setAttribute("aria-current", i === index ? "true" : "false");
    });
    var active = strip.children[index];
    if (active) active.scrollIntoView({ block: "nearest", inline: "center" });
  }

  function open(link) {
    if (!viewer) build();
    var grid = link.closest(".shot-grid");
    links = Array.prototype.slice.call(grid.querySelectorAll("a[href]"));
    strip.innerHTML = "";
    links.forEach(function (l, i) {
      var tile = document.createElement("button");
      tile.type = "button";
      tile.className = "shot-viewer-tile";
      tile.dataset.index = i;
      tile.title = labelFor(l);
      var thumb = document.createElement("img");
      thumb.src = l.href;
      thumb.alt = "";
      thumb.loading = "lazy";
      tile.appendChild(thumb);
      strip.appendChild(tile);
    });
    lastFocus = document.activeElement;
    viewer.hidden = false;
    document.body.classList.add("shot-viewer-open-body");
    show(links.indexOf(link));
    viewer.querySelector(".shot-viewer-close").focus();
  }

  function close() {
    if (!viewer || viewer.hidden) return;
    viewer.hidden = true;
    img.removeAttribute("src");
    document.body.classList.remove("shot-viewer-open-body");
    if (lastFocus && lastFocus.focus) lastFocus.focus();
  }

  // Delegated, so thumbnails added live on the submitting page work too.
  document.addEventListener("click", function (e) {
    var link = e.target.closest && e.target.closest(".shot-grid a[href]");
    if (!link || e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    open(link);
  });

  document.addEventListener("keydown", function (e) {
    if (!viewer || viewer.hidden) return;
    if (e.key === "Escape") { e.stopPropagation(); close(); }
    else if (e.key === "ArrowLeft") { e.preventDefault(); show(index - 1); }
    else if (e.key === "ArrowRight") { e.preventDefault(); show(index + 1); }
  }, true);
})();
