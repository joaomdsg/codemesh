// The diagnosis island: the module layout coloured by a lens, with a marker
// on each place that needs attention. Hovering a marker shows its short
// prognosis; clicking opens the full one. Clicking a tile rings what calls
// it, up to three hops. The server owns every fact; this only draws.
//
// Modes: "full" is the explorable map; "mini" frames the open prognosis and
// outlines its related places.
(() => {
  const cm = (window.codemesh = window.codemesh || {});
  const K = 0, X = 1, Y = 2, W = 3, H = 4, NAME = 5, ID = 8, EXP = 9;
  const levelWord = { 3: "Fix first", 2: "Fix soon", 1: "When convenient" };

  cm.diag = (el, d, opts) => {
    if (!window.d3 || !d || !d.tiles) return;
    const sig = [d.at, d.lens, d.focus, (d.ring || []).join("|")].join("/");
    if (el.__dx && el.__dx.sig === sig) return;
    el.replaceChildren();
    el.__dx = draw(el, d, opts.mode || "full");
    el.__dx.sig = sig;
  };

  function draw(el, d, mode) {
    const heat = d.heats[d.lens] || [];
    const byId = new Map(d.tiles.map((t, i) => [t[ID], i]));
    const svg = d3.select(el).append("svg")
      .attr("class", "atlas-svg")
      .attr("viewBox", `0 0 ${d.w} ${d.h}`)
      .attr("role", "img")
      .attr("aria-label", "Map of the module. Markers sit on places that need attention.");
    const g = svg.append("g");
    const s = { svg, g, k: 1, w: d.w, sel: -1 };

    // A frame's tab shows the worst heat inside it, so a hot declaration too
    // small to see still shows from the module view.
    const worst = d.tiles.map(() => 0);
    let pi = -1, fi = -1;
    d.tiles.forEach((t, i) => {
      if (t[K] === "p") { pi = i; fi = -1; }
      else if (t[K] === "f") fi = i;
      else {
        const v = heat[i] || 0;
        if (pi >= 0) worst[pi] = Math.max(worst[pi], v);
        if (fi >= 0) worst[fi] = Math.max(worst[fi], v);
      }
    });
    const fileOf = new Map();
    let file = null;
    d.tiles.forEach((t) => { if (t[K] === "f") file = t; if (t[K] === "d") fileOf.set(t, file); });

    s.rects = g.selectAll("rect.tile").data(d.tiles).join("rect")
      .attr("class", (t, i) => "tile t-" + t[K] + (t[K] === "d" ? " h" + (heat[i] || 0) : ""))
      .attr("x", (t) => t[X]).attr("y", (t) => t[Y])
      .attr("width", (t) => t[W]).attr("height", (t) => t[H]);
    s.rects.append("title").text((t) => t[NAME]);
    s.bars = bars(g, d.tiles);

    const framed = d.tiles.map((t, i) => [t, i]).filter(([t, i]) => t[K] !== "d" && worst[i] >= 3);
    s.tabs = g.selectAll("rect.dx-tab").data(framed).join("rect")
      .attr("class", ([, i]) => "dx-tab h" + worst[i]);

    // A label on the hottest colour is white, which reads better there than ink.
    s.labels = g.selectAll("text").data(d.tiles).join("text")
      .attr("class", (t, i) => labelClass(t) + (t[K] === "d" && heat[i] >= 5 ? " hot" : ""))
      .text((t) => t[NAME]);

    const marks = d.marks.filter((m) => m.lens === d.lens);
    s.marks = g.selectAll("g.mk").data(marks).join("g")
      .attr("class", (m) => "mk mk-" + m.l + (d.focus && d.tiles[m.t][ID] === d.focus ? " cur" : ""))
      .attr("tabindex", 0)
      .attr("role", "link")
      .attr("aria-label", (m) => levelWord[m.l] + ": " + m.title + ", " + m.name);
    s.marks.append("circle");

    const tip = d3.select(el).append("div").attr("class", "dx-tip").attr("hidden", true);
    const open = (m) => { location.href = "/prognosis/" + encodeURIComponent(m.key); };
    s.marks
      .on("mouseenter focus", (e, m) => {
        tip.attr("hidden", null).html("");
        tip.append("div").attr("class", "dx-tip-level tl-" + m.l).text(levelWord[m.l]);
        tip.append("div").attr("class", "dx-tip-title").text(m.title);
        tip.append("div").attr("class", "dx-tip-name").text(m.name);
        tip.append("div").attr("class", "dx-tip-sum").text(m.sum);
        tip.append("div").attr("class", "dx-tip-hint").text("Click for the full prognosis");
        place(el, tip.node(), e.currentTarget);
      })
      .on("mouseleave blur", () => tip.attr("hidden", true));
    s.marks.on("click", (e, m) => { e.stopPropagation(); open(m); })
      .on("keydown", (e, m) => { if (e.key === "Enter") open(m); });

    // Who calls whom, for the rings.
    const callers = new Map();
    for (const [from, to] of d.calls) {
      if (!callers.has(to)) callers.set(to, []);
      callers.get(to).push(from);
    }
    if (mode === "full") {
      s.rects.on("click", (e, t) => ring(s, callers, s.sel === d.tiles.indexOf(t) ? -1 : d.tiles.indexOf(t)));
      el.__clear = () => ring(s, callers, -1);
    }

    const ringSet = new Set((d.ring || []).map((id) => byId.get(id)).filter((i) => i !== undefined));
    s.rects.classed("rel", (t, i) => ringSet.has(i));
    const focus = byId.get(d.focus);
    s.rects.classed("focus", (t, i) => i === focus);
    // A stroke is centred on the edge, so tiles and strips drawn later would
    // cover half of an outline: outlines are drawn again, unfilled, above them.
    const lined = s.rects.filter((t, i) => ringSet.has(i) || i === focus).nodes();
    g.insert("g", "rect.dx-tab, text").attr("class", "dx-lines").selectAll("rect")
      .data(lined).join("rect")
      .attr("class", (n) => n.getAttribute("class"))
      .attr("x", (n) => n.getAttribute("x")).attr("y", (n) => n.getAttribute("y"))
      .attr("width", (n) => n.getAttribute("width")).attr("height", (n) => n.getAttribute("height"));

    s.zoom = d3.zoom().scaleExtent([1, 80]).translateExtent([[0, 0], [d.w, d.h]])
      .on("zoom", (e) => { g.attr("transform", e.transform); s.k = e.transform.k; scale(s, d); });
    svg.call(s.zoom).on("dblclick.zoom", null);
    svg.on("dblclick", () => svg.transition().duration(300).call(s.zoom.transform, d3.zoomIdentity));
    scale(s, d);
    if (focus !== undefined) fly(s, d, d.tiles[focus], fileOf);

    if (mode === "mini") {
      // The related list beside the map points into it.
      document.querySelectorAll("[data-ref]").forEach((li) => {
        const i = byId.get(li.dataset.ref);
        if (i === undefined) return;
        li.addEventListener("mouseenter", () => s.rects.classed("hl", (t, j) => j === i));
        li.addEventListener("mouseleave", () => s.rects.classed("hl", false));
        li.addEventListener("click", (e) => { if (!e.target.closest("a")) fly(s, d, d.tiles[i], fileOf); });
      });
    }
    return s;
  }

  // ring marks the selection's callers by hop and fades the rest.
  function ring(s, callers, sel) {
    s.sel = sel;
    const hop = new Map();
    if (sel >= 0) {
      hop.set(sel, 0);
      let edge = [sel];
      for (let n = 1; n <= 3 && edge.length; n++) {
        const next = [];
        for (const i of edge) for (const c of callers.get(i) || []) if (!hop.has(c)) { hop.set(c, n); next.push(c); }
        edge = next;
      }
    }
    s.rects
      .classed("sel", (t, i) => i === sel)
      .classed("r1", (t, i) => hop.get(i) === 1)
      .classed("r2", (t, i) => hop.get(i) === 2)
      .classed("r3", (t, i) => hop.get(i) === 3)
      .classed("dim", (t, i) => sel >= 0 && t[K] === "d" && !hop.has(i));
  }

  // scale keeps markers, tabs and labels a fixed size on screen at any zoom.
  function scale(s, d) {
    const ppu = (s.svg.node().clientWidth || s.w) / s.w * s.k;
    const u = (px) => px / ppu;
    s.marks
      .attr("transform", (m) => { const t = d.tiles[m.t]; return `translate(${t[X] + t[W] - u(9)},${t[Y] + u(9)})`; })
      .select("circle").attr("r", (m) => u(3 + 1.5 * m.l)).attr("stroke-width", u(1.5));
    s.tabs
      .attr("x", ([t]) => t[X] + t[W] - u(14)).attr("y", ([t]) => t[Y] + u(2))
      .attr("width", u(10)).attr("height", u(6));
    placeBars(s.bars, u);
    labels(s.labels, ppu);
  }

  // bars marks exported declarations with a strip along their top edge.
  function bars(g, tiles) {
    return g.selectAll("rect.exp-bar").data(tiles.filter((t) => t[EXP])).join("rect").attr("class", "exp-bar");
  }

  function placeBars(sel, u) {
    sel.attr("x", (t) => t[X]).attr("y", (t) => t[Y]).attr("width", (t) => t[W])
      .attr("height", (t) => Math.min(u(3), t[H] / 3));
  }

  function labelClass(t) {
    return "atlas-label l-" + t[K] + (t[EXP] ? " exp" : "");
  }

  // labels sizes text to 11 screen pixels and shows a label only when it fits
  // its tile on screen and clears the package and file labels around it.
  // Tiles come in layout order, each frame before what it holds.
  function labels(sel, ppu) {
    const u = (px) => px / ppu, px = u(11);
    let pkg = null, file = null;
    const box = (t) => ({ x0: t[X], y0: t[Y], x1: t[X] + u(t[NAME].length * 7 + 6), y1: t[Y] + px + u(5) });
    const clear = (b, o) => !o || b.x1 <= o.x0 || o.x1 <= b.x0 || b.y1 <= o.y0 || o.y1 <= b.y0;
    const shown = new Map();
    sel.each((t) => {
      const fits = t[W] * ppu > t[NAME].length * 7 + 22 && t[H] * ppu > 16;
      const b = box(t);
      const ok = fits && clear(b, pkg) && (t[K] === "p" || clear(b, file));
      if (t[K] === "p") { pkg = ok ? b : null; file = null; }
      if (t[K] === "f") file = ok ? b : null;
      shown.set(t, ok);
    });
    sel.attr("font-size", px).attr("x", (t) => t[X] + u(3)).attr("y", (t) => t[Y] + px + u(2))
      .attr("display", (t) => (shown.get(t) ? null : "none"));
  }

  function fly(s, d, t, fileOf) {
    const f = (t[K] === "d" && fileOf.get(t)) || t;
    const k = Math.max(1, Math.min(80, (d.w * 0.8) / f[W], (d.h * 0.8) / f[H]));
    // A programmatic transform skips the zoom's translate extent, so it is
    // clamped here: centring a place near an edge would leave a blank band.
    const clamp = (v, size) => Math.min(0, Math.max(size - size * k, v));
    const to = d3.zoomIdentity
      .translate(clamp(d.w / 2 - k * (f[X] + f[W] / 2), d.w), clamp(d.h / 2 - k * (f[Y] + f[H] / 2), d.h)).scale(k);
    const still = matchMedia("(prefers-reduced-motion: reduce)").matches;
    (still ? s.svg : s.svg.transition().duration(450)).call(s.zoom.transform, to);
  }

  // place puts the tip beside the marker, inside the map.
  function place(el, tip, at) {
    const box = el.getBoundingClientRect(), m = at.getBoundingClientRect();
    let x = m.right - box.left + 8, y = m.top - box.top - 4;
    if (x + tip.offsetWidth > box.width) x = m.left - box.left - tip.offsetWidth - 8;
    y = Math.max(0, Math.min(y, box.height - tip.offsetHeight));
    tip.style.left = Math.max(0, x) + "px";
    tip.style.top = y + "px";
  }

  // Escape works wherever focus is, short of a field being typed in: a tile
  // click leaves focus on the page, not on the map.
  document.addEventListener("keydown", (e) => {
    const t = e.target;
    if (e.altKey || e.ctrlKey || e.metaKey || t.closest && t.closest("input, textarea, select, [contenteditable]")) return;
    if (e.key === "Escape") document.querySelectorAll(".dx-map").forEach((m) => m.__clear && m.__clear());
  });
})();
