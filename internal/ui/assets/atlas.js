// The atlas island: D3 draws the module layout the server sends, zooms it,
// and links its tiles. A Datastar effect calls codemesh.atlas whenever one of
// its signals changes; the server owns every fact, the browser only the view.
//
// Review: opts.reviewed lists reviewed cards, opts.focus is the focused card.
// Lit tiles open their card. Map: tiles carry a lens heat, opts.focus is the
// selected tile's id, and a click opens that tile on the map page.
(() => {
  const cm = (window.codemesh = window.codemesh || {});
  const K = 0, X = 1, Y = 2, W = 3, H = 4, NAME = 5, UNIT = 6, HEAT = 7, ID = 8;

  cm.atlas = (el, atlas, opts) => {
    if (!window.d3 || !atlas || !atlas.tiles) return;
    let s = el.__atlas;
    if (!s) s = el.__atlas = mount(el, atlas);
    if (s.at !== atlas.at || s.lens !== atlas.lens) {
      draw(s, atlas);
      s.at = atlas.at;
      s.lens = atlas.lens;
      s.focus = "";
    }
    const focus = opts.focus || "";
    if (opts.reviewed) {
      const done = new Set(opts.reviewed);
      s.decls
        .classed("open", (d) => d[UNIT] !== "" && !done.has(d[UNIT]))
        .classed("done", (d) => done.has(d[UNIT]));
    }
    s.rects.classed("cur", (d) => focus !== "" && (d[UNIT] === focus || d[ID] === focus));
    if (focus && focus !== s.focus) fly(s, atlas, focus);
    s.focus = focus;
  };

  function mount(el, atlas) {
    const svg = d3.select(el).append("svg")
      .attr("class", "atlas-svg")
      .attr("viewBox", `0 0 ${atlas.w} ${atlas.h}`)
      .attr("role", "img")
      .attr("aria-label", atlas.lens
        ? "Map of the module coloured by " + atlas.lens + ". Click a tile to open it."
        : "Map of the module. Lit tiles are declarations in this change; click one to open its card.");
    const g = svg.append("g");
    const s = { svg, g, at: null, lens: null, focus: "", rects: d3.select(null), decls: d3.select(null), labels: null, k: 1, w: atlas.w };
    s.zoom = d3.zoom()
      .scaleExtent([1, 80])
      .translateExtent([[0, 0], [atlas.w, atlas.h]])
      .on("zoom", (e) => {
        g.attr("transform", e.transform);
        s.k = e.transform.k;
        label(s);
      });
    svg.call(s.zoom).on("dblclick.zoom", null);
    svg.on("dblclick", () => svg.transition().duration(300).call(s.zoom.transform, d3.zoomIdentity));
    return s;
  }

  function draw(s, atlas) {
    s.g.selectAll("*").remove();
    const tiles = atlas.tiles;
    // Each declaration's file: the last file tile before it in layout order.
    const fileOf = new Map();
    let file = null;
    for (const t of tiles) {
      if (t[K] === "f") file = t;
      if (t[K] === "d") fileOf.set(t, file);
    }
    s.fileOf = fileOf;
    s.rects = s.g.selectAll("rect").data(tiles).join("rect")
      .attr("class", (d) => "t-" + d[K] + (d[UNIT] ? " lit" : "") + (atlas.lens && d[K] === "d" ? " h" + d[HEAT] : ""))
      .attr("x", (d) => d[X]).attr("y", (d) => d[Y])
      .attr("width", (d) => d[W]).attr("height", (d) => d[H]);
    s.rects.append("title").text((d) => d[NAME]);
    if (atlas.lens) {
      s.rects.on("click", (e, d) => { location.href = href(atlas.lens, d, fileOf.get(d)); });
    } else {
      s.rects.filter((d) => d[UNIT]).on("click", (e, d) => {
        const card = document.getElementById(d[UNIT]);
        if (!card) return;
        card.focus({ preventScroll: true });
        card.scrollIntoView({ block: "start" });
      });
    }
    s.decls = s.rects.filter((d) => d[K] === "d");
    // The review labels only lit declarations; the rest would be thousands of
    // nodes nobody reads there.
    s.labels = s.g.selectAll("text")
      .data(tiles.filter((d) => d[K] !== "d" || atlas.lens || d[UNIT]))
      .join("text")
      .attr("class", (d) => "atlas-label l-" + d[K] + (atlas.lens && d[K] === "d" && d[HEAT] >= 4 ? " hot" : ""))
      .text((d) => d[NAME]);
    label(s);
  }

  function href(lens, d, file) {
    const q = ["lens=" + encodeURIComponent(lens)];
    if (d[K] === "d" && file) q.push("in=" + encodeURIComponent(file[ID]), "d=" + encodeURIComponent(d[ID]));
    else q.push("in=" + encodeURIComponent(d[ID]));
    return "/?" + q.join("&");
  }

  // label sizes text to 11 screen pixels at any zoom and shows only labels
  // that fit their tile on screen and clear the labels of the package and
  // file around them: a frame's label strip is fixed in layout units, so
  // zoomed out it is thinner than the text.
  function label(s) {
    if (!s.labels) return;
    // Screen pixels per layout unit: the viewBox is scaled into the panel.
    const ppu = (s.svg.node().clientWidth || s.w) / s.w * s.k;
    const px = 11 / ppu;
    // Tiles come in layout order, each package then its files, each file
    // then its declarations, so the last shown frame labels are the parents.
    let pkg = null, file = null;
    // The text's box, as the browser measures it: 2px below the tile top to
    // the descent.
    const box = (d) => ({ x0: d[X], y0: d[Y] + 2 / ppu, x1: d[X] + (d[NAME].length * 7 + 6) / ppu, y1: d[Y] + px + 5 / ppu });
    const clear = (b, o) => !o || b.x1 <= o.x0 || o.x1 <= b.x0 || b.y1 <= o.y0 || o.y1 <= b.y0;
    const shown = new Map();
    s.labels.each((d) => {
      const fits = d[W] * ppu > d[NAME].length * 7 + 6 && d[H] * ppu > 16;
      const b = box(d);
      const ok = fits && clear(b, pkg) && (d[K] === "p" || clear(b, file));
      if (d[K] === "p") { pkg = ok ? b : null; file = null; }
      if (d[K] === "f") file = ok ? b : null;
      shown.set(d, ok);
    });
    s.labels
      .attr("font-size", px)
      .attr("x", (d) => d[X] + 3 / ppu)
      .attr("y", (d) => d[Y] + px + 2 / ppu)
      .attr("display", (d) => (shown.get(d) ? null : "none"));
  }

  // fly frames the focus with a margin: a declaration is framed by its file,
  // so it is read among its neighbours; a file or package by itself.
  function fly(s, atlas, focus) {
    const t = atlas.tiles.find((d) => d[UNIT] === focus || d[ID] === focus);
    if (!t) return;
    const f = (t[K] === "d" && s.fileOf.get(t)) || t;
    const k = Math.max(1, Math.min(80, (atlas.w * 0.85) / f[W], (atlas.h * 0.85) / f[H]));
    const to = d3.zoomIdentity
      .translate(atlas.w / 2, atlas.h / 2)
      .scale(k)
      .translate(-(f[X] + f[W] / 2), -(f[Y] + f[H] / 2));
    const still = matchMedia("(prefers-reduced-motion: reduce)").matches;
    (still ? s.svg : s.svg.transition().duration(450)).call(s.zoom.transform, to);
  }
})();
