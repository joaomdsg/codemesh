// The diagnosis island: the module layout coloured by a lens, with a marker
// on each place that needs attention. Hovering a marker shows its short
// prognosis; clicking opens the full one. Clicking a tile rings what calls
// it, up to three hops. The server owns every fact; this only draws.
//
// Modes: "full" is the explorable map; "mini" frames the open prognosis and
// outlines its related places; "compare" frames it and outlines changed code.
(() => {
  const cm = (window.codemesh = window.codemesh || {});
  const K = 0, X = 1, Y = 2, W = 3, H = 4, NAME = 5, ID = 8, EXP = 9;
  const levelWord = { 3: "Fix first", 2: "Fix soon", 1: "When convenient" };

  cm.diag = (el, d, opts) => {
    if (!window.d3 || !d || !d.tiles) return;
    const sig = [d.at, d.lens, d.focus, (d.ring || []).join("|"), (d.broke || []).join("|")].join("/");
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
      .attr("tabindex", mode === "compare" ? null : 0)
      .attr("role", mode === "compare" ? null : "link")
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
        if (mode !== "compare") tip.append("div").attr("class", "dx-tip-hint").text("Click for the full prognosis");
        place(el, tip.node(), e.currentTarget);
      })
      .on("mouseleave blur", () => tip.attr("hidden", true));
    if (mode !== "compare") {
      s.marks.on("click", (e, m) => { e.stopPropagation(); open(m); })
        .on("keydown", (e, m) => { if (e.key === "Enter") open(m); });
    }

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
    s.rects.classed(mode === "compare" ? "chg" : "rel", (t, i) => ringSet.has(i));
    const broke = new Set((d.broke || []).map((id) => byId.get(id)));
    s.rects.classed("broke", (t, i) => broke.has(i));
    // The red outline already says exported; a strip would hide its top edge.
    const unbarred = (t) => broke.has(byId.get(t[ID]));
    s.bars.filter(unbarred).remove();
    s.bars = s.bars.filter((t) => !unbarred(t));
    const focus = byId.get(d.focus);
    s.rects.classed("focus", (t, i) => i === focus);
    // A stroke is centred on the edge, so tiles and strips drawn later would
    // cover half of an outline: outlines are drawn again, unfilled, above them.
    const lined = s.rects.filter((t, i) => broke.has(i) || ringSet.has(i) || i === focus).nodes();
    g.insert("g", "rect.dx-tab, text").attr("class", "dx-lines").selectAll("rect")
      .data(lined).join("rect")
      .attr("class", (n) => n.getAttribute("class"))
      .attr("x", (n) => n.getAttribute("x")).attr("y", (n) => n.getAttribute("y"))
      .attr("width", (n) => n.getAttribute("width")).attr("height", (n) => n.getAttribute("height"));

    s.zoom = d3.zoom().scaleExtent([1, 80]).translateExtent([[0, 0], [d.w, d.h]])
      .on("zoom", (e) => {
        g.attr("transform", e.transform); s.k = e.transform.k; scale(s, d);
        if (mode === "compare" && e.sourceEvent) follow(s, e.transform);
      });
    if (mode === "compare") compared.add(s);
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

  // The before and after maps share one coordinate space, so a pan or zoom on
  // either is applied to the other: the reader compares the same place. Where
  // the change added or removed declarations, the tiles there shift.
  const compared = new Set();
  function follow(from, t) {
    for (const o of compared) {
      if (!o.svg.node().isConnected) { compared.delete(o); continue; }
      if (o !== from) o.svg.call(o.zoom.transform, t);
    }
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

  // Keys work wherever focus is, short of a field being typed in: a tile
  // click leaves focus on the page, not on the map. Escape clears caller
  // rings; ← and → step through a replay.
  document.addEventListener("keydown", (e) => {
    const t = e.target;
    if (e.altKey || e.ctrlKey || e.metaKey || t.closest && t.closest("input, textarea, select, [contenteditable]")) return;
    if (e.key === "Escape") document.querySelectorAll(".dx-map").forEach((m) => m.__clear && m.__clear());
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    const rp = document.querySelector(".rp");
    if (!rp || !rp.__rp || !rp.__rp.step) return;
    e.preventDefault();
    rp.__rp.step(e.key === "ArrowRight" ? 1 : -1);
  });

  // The replay: what Claude did over time, on the map it started from. The
  // map, the swimlanes and the step list share one playhead; moving any of
  // them moves the others. While a run is live the playhead follows the
  // newest step until the reader takes it.
  const LANES = ["think", "read", "search", "edit", "run", "other"];
  const LANE_LABEL = { think: "think", read: "read", search: "search", edit: "edit", run: "run", other: "other" };

  cm.replay = (el, r) => {
    if (!window.d3 || !r || !r.map || !r.map.tiles) return;
    let s = el.__rp;
    if (!s) {
      el.replaceChildren();
      s = el.__rp = mountReplay(el, r.map);
      s.at = r.map.at;
    } else if (s.at !== r.map.at) {
      drawMap(s, r.map);
      s.at = r.map.at;
    }
    s.r = r;
    const last = r.steps.length - 1;
    if (s.follow || s.i > last) s.i = last;
    lanes(s);
    list(s);
    show(s, "data");
  };

  // drawMap draws the map the footprint goes on: neutral tiles, so the
  // footprint is the only colour on it. A redraw for a tree with new files
  // keeps the reader's zoom.
  function drawMap(s, d) {
    const was = s.svg && d3.zoomTransform(s.svg.node());
    s.mapBox.selectAll("svg").remove();
    s.d = d;
    const svg = s.mapBox.append("svg").attr("class", "atlas-svg").attr("viewBox", `0 0 ${d.w} ${d.h}`)
      .attr("role", "img").attr("aria-label", "Map of the module; files Claude read and edited are coloured.");
    const g = svg.append("g");
    s.rects = g.selectAll("rect").data(d.tiles).join("rect")
      .attr("class", (t) => "t-" + t[K] + (t[K] === "d" ? " h0" : ""))
      .attr("x", (t) => t[X]).attr("y", (t) => t[Y]).attr("width", (t) => t[W]).attr("height", (t) => t[H]);
    s.rects.append("title").text((t) => t[NAME]);
    s.bars = bars(g, d.tiles);
    // Each file's declarations take its footprint colour; the file tile
    // itself sits under them.
    s.declsOf = new Map();
    let file = null;
    d.tiles.forEach((t, i) => {
      if (t[K] === "f") { file = t[ID]; s.declsOf.set(file, []); }
      else if (t[K] === "d" && file) s.declsOf.get(file).push(i);
      else if (t[K] === "p") file = null;
    });
    s.byId = new Map(d.tiles.map((t, i) => [t[ID], i]));
    s.fileTile = new Map(d.tiles.map((t, i) => [t, i]).filter(([t]) => t[K] === "f").map(([t, i]) => [t[ID], i]));
    s.labels = g.selectAll("text.atlas-label").data(d.tiles).join("text")
      .attr("class", labelClass).text((t) => t[NAME]);
    s.counts = g.append("g").attr("class", "rp-counts");
    s.cur = g.append("rect").attr("class", "rp-cur").attr("display", "none");
    s.svg = svg; s.g = g; s.w = d.w;
    s.zoom = d3.zoom().scaleExtent([1, 80]).translateExtent([[0, 0], [d.w, d.h]])
      .on("zoom", (e) => { g.attr("transform", e.transform); s.k = e.transform.k; rescale(s); });
    svg.call(s.zoom).on("dblclick.zoom", null);
    svg.on("dblclick", () => svg.transition().duration(300).call(s.zoom.transform, d3.zoomIdentity));
    if (was) svg.call(s.zoom.transform, was);
  }

  function mountReplay(el, d) {
    const root = d3.select(el);
    const top = root.append("div").attr("class", "rp-top");
    const mapBox = top.append("div").attr("class", "rp-map atlas atlas-map");
    const listBox = top.append("div").attr("class", "rp-list");
    const bar = root.append("div").attr("class", "rp-bar");
    const lanesBox = root.append("div").attr("class", "rp-lanes");
    const s = { el, d, i: -1, follow: true, mapBox, listBox, bar, lanesBox, k: 1 };
    drawMap(s, d);


    // The bar: where the playhead is, the follow switch, and new files.
    s.where = bar.append("span").attr("class", "rp-where");
    bar.append("span").attr("class", "rp-keys hint").text("← → step · drag the strip under the lanes to zoom");
    s.followBtn = bar.append("button").attr("class", "btn rp-follow").text("Follow live")
      .on("click", () => { s.follow = true; s.i = s.r.steps.length - 1; show(s, "follow"); });
    s.newFiles = bar.append("span").attr("class", "rp-new");
    // The footprint's legend: what each colour on this map means.
    const key = bar.append("span").attr("class", "rp-key").attr("aria-hidden", "true");
    const item = (cls, text) => { const i = key.append("span").attr("class", "ramp-end"); i.append("span").attr("class", "sw " + cls); i.append("span").text(text); };
    item("sw-read", "read");
    const ed = key.append("span").attr("class", "ramp-end");
    ed.append("span").text("edited: 1 line");
    const sws = ed.append("span").attr("class", "ramp-sw");
    for (const n of [1, 2, 3, 4]) sws.append("span").attr("class", "sw sw-edit" + n);
    ed.append("span").text("most lines");
    item("sw-file", "rest of an edited file");
    item("sw-cur", "this step");

    // Lanes and their overview.
    s.lsvg = lanesBox.append("svg").attr("class", "rp-lsvg");
    s.osvg = lanesBox.append("svg").attr("class", "rp-osvg");

    s.step = (by) => {
      if (!s.r || !s.r.steps.length) return;
      s.follow = false;
      s.i = Math.max(0, Math.min(s.r.steps.length - 1, s.i + by));
      show(s, "key");
    };
    // Scrolling the list moves the playhead to the step at its top.
    let ticking = false;
    s.listBox.on("scroll", () => {
      if (s.scrolling || ticking) return;
      ticking = true;
      requestAnimationFrame(() => {
        ticking = false;
        const top = s.listBox.node().getBoundingClientRect().top;
        const cards = s.listBox.selectAll(".rp-card").nodes();
        const at = cards.findIndex((c) => c.getBoundingClientRect().bottom > top + 8);
        if (at >= 0 && at !== s.i) { s.follow = false; s.i = at; show(s, "list"); }
      });
    });
    new ResizeObserver(() => { if (s.r) { lanes(s); show(s, "resize"); } }).observe(lanesBox.node());
    return s;
  }

  const clock = (ms) => {
    const t = Math.round(ms / 1000);
    return Math.floor(t / 60) + ":" + String(t % 60).padStart(2, "0");
  };
  const span = (st) => Math.max(0, st.t1 - st.t0);

  // lanes draws the swimlanes, and the overview whose brush zooms them.
  function lanes(s) {
    const steps = s.r.steps;
    const W = Math.max(320, s.lanesBox.node().clientWidth);
    const label = 64, laneH = 22, H = LANES.length * laneH + 22, OH = 34;
    // The lanes start at Claude's first step: the wait before it, for the
    // starting point's checks, would squeeze every step to the right.
    const start = steps.length ? Math.max(0, steps[0].t0 - 1000) : 0;
    const end = Math.max(s.r.now || 0, ...steps.map((st) => st.t1), start + 1000);
    const full = d3.scaleLinear().domain([start, end]).range([label, W - 8]);
    s.x = s.x && s.zoomed ? s.x.range([label, W - 8]) : full.copy();
    s.full = full;
    s.lsvg.attr("viewBox", `0 0 ${W} ${H}`).attr("width", W).attr("height", H);
    s.osvg.attr("viewBox", `0 0 ${W} ${OH}`).attr("width", W).attr("height", OH);
    const y = (k) => LANES.indexOf(k) * laneH;

    s.lsvg.selectAll("g.rp-lane").data(LANES).join((e) => {
      const g = e.append("g").attr("class", "rp-lane");
      g.append("rect").attr("class", "rp-lane-bg");
      g.append("text").attr("class", "rp-lane-label");
      return g;
    })
      .attr("transform", (k) => `translate(0,${y(k)})`)
      .call((g) => g.select("rect").attr("x", label).attr("width", W - label - 8).attr("height", laneH - 4))
      .call((g) => g.select("text").attr("x", 0).attr("y", laneH / 2 + 2).text((k) => LANE_LABEL[k]));

    // Marks zoomed past either end are clipped to the lanes, not the labels.
    if (!s.markG) {
      const id = "rp-clip-" + Math.random().toString(36).slice(2);
      s.clip = s.lsvg.append("clipPath").attr("id", id).append("rect");
      s.markG = s.lsvg.append("g").attr("clip-path", `url(#${id})`);
    }
    s.clip.attr("x", label).attr("y", 0).attr("width", W - label - 8).attr("height", H);
    const draw = () => {
      s.markG.selectAll("rect.rp-mark").data(steps).join("rect")
        .attr("class", (st, i) => "rp-mark rk-" + st.k + (st.fail ? " fail" : "") + (i === s.i ? " on" : ""))
        .attr("x", (st) => s.x(st.t0))
        .attr("y", (st) => y(LANES.includes(st.k) ? st.k : "other") + 2)
        .attr("width", (st) => Math.max(3, s.x(st.t0 + span(st)) - s.x(st.t0)))
        .attr("height", laneH - 8)
        .on("click", (e, st) => { s.follow = false; s.i = steps.indexOf(st); show(s, "lane"); });
      const ticks = s.x.ticks(Math.max(2, Math.floor(W / 120)));
      s.lsvg.selectAll("text.rp-tick").data(ticks).join("text").attr("class", "rp-tick")
        .attr("x", (t) => s.x(t)).attr("y", H - 4).text((t) => clock(t));
      head(s, H);
    };
    s.drawLanes = draw;

    // Drag anywhere on the lanes to scrub. The drag is made once: a new one
    // per live update would drop a touch drag already under way, since each
    // d3.drag keeps its own gestures.
    if (!s.scrub) {
      s.scrub = d3.drag().on("start drag", (e) => {
        const steps = s.r.steps;
        if (!steps.length) return;
        const t = s.x.invert(e.x);
        let at = d3.bisector((st) => st.t0).right(steps, t) - 1;
        at = Math.max(0, Math.min(steps.length - 1, at));
        if (at !== s.i) { s.follow = false; s.i = at; show(s, "lane"); }
      });
      s.lsvg.call(s.scrub);
    }

    s.osvg.selectAll("rect.rp-omark").data(steps).join("rect")
      .attr("class", (st) => "rp-omark rk-" + st.k)
      .attr("x", (st) => full(st.t0)).attr("width", (st) => Math.max(1, full(st.t0 + span(st)) - full(st.t0)))
      .attr("y", (st) => 2 + LANES.indexOf(LANES.includes(st.k) ? st.k : "other") * 4).attr("height", 3);
    if (!s.brush) {
      s.brush = d3.brushX().on("brush end", (e) => {
        if (!e.sourceEvent) return;
        s.zoomed = !!e.selection;
        s.x = e.selection ? d3.scaleLinear().domain(e.selection.map(s.full.invert)).range(s.full.range()) : s.full.copy();
        s.drawLanes();
      });
      s.osvg.append("g").attr("class", "rp-brush");
    }
    s.brush.extent([[label, 0], [W - 8, OH]]);
    s.osvg.select("g.rp-brush").call(s.brush);
    draw();
  }

  function head(s, H) {
    const st = s.r.steps[s.i];
    s.lsvg.selectAll("line.rp-head").data(st ? [st] : []).join("line").attr("class", "rp-head")
      .attr("x1", (d) => s.x(d.t0)).attr("x2", (d) => s.x(d.t0)).attr("y1", 0).attr("y2", H - 14);
    s.lsvg.selectAll("rect.rp-mark").classed("on", (d, i) => i === s.i);
  }

  // list renders one card per step; only cards whose content changed are
  // rebuilt, so a live run does not jump the reader's scroll.
  function list(s) {
    const steps = s.r.steps;
    s.listBox.selectAll("div.rp-empty").data(steps.length ? [] : [0]).join("div").attr("class", "rp-empty")
      .text("Claude has not acted yet.");
    s.listBox.selectAll("article.rp-card").data(steps).join("article")
      .attr("class", (st, i) => "rp-card rk-" + st.k + (st.fail ? " fail" : ""))
      .each(function (st, i) {
        const sig = [st.t1, st.out ? st.out.length : 0, st.fail].join("/");
        if (this.__sig === sig) return;
        this.__sig = sig;
        card(d3.select(this).html(""), st, i);
      })
      .on("click", function (e, st) {
        if (e.target.closest("pre, summary")) return;
        s.follow = false; s.i = steps.indexOf(st); show(s, "card");
      });
  }

  // A step that changed files is titled by them, not by the command that
  // made the change: the diff is what the reader came for.
  const titleOf = (st) => {
    const ds = st.diffs || [];
    if (!ds.length) return st.title;
    const names = [...new Set(ds.map((d) => d.path.split("/").pop()))];
    return "Edit " + names.join(", ");
  };

  function card(c, st, i) {
    const h = c.append("header");
    h.append("span").attr("class", "rp-no").text(i + 1);
    h.append("span").attr("class", "rp-kind").text(st.k);
    h.append("span").attr("class", "rp-title").attr("title", titleOf(st)).text(titleOf(st));
    h.append("span").attr("class", "rp-time").text(clock(st.t0) + (span(st) >= 1000 ? " · " + clock(span(st)) : ""));
    if (st.path && st.k !== "edit") c.append("div").attr("class", "rp-path").text(st.path);
    if (st.reads && st.reads.length) c.append("div").attr("class", "rp-path").text("reads " + st.reads.join(", "));
    // A thought whose title already says it all needs no body.
    if (st.k === "think" && st.text !== st.title) c.append("p").attr("class", "rp-text").text(st.text);
    for (const ch of st.diffs || []) {
      c.append("div").attr("class", "rp-path").text(ch.path + ":" + Math.max(1, ch.at) + (ch.file ? "" : " · outside the module"));
      const pre = c.append("pre").attr("class", "rp-diff");
      for (const [op, line] of diffLines(ch.old || "", ch.new || "")) {
        pre.append("span").attr("class", "rp-dl rp-dl-" + (op === "+" ? "add" : op === "-" ? "del" : "ctx")).text(op + " " + line + "\n");
      }
    }
    if (st.k === "edit" && !(st.diffs && st.diffs.length) && (st.old || st.new)) {
      c.append("div").attr("class", "rp-path").text(st.path + (st.file ? "" : " · outside the module"));
      const pre = c.append("pre").attr("class", "rp-diff");
      for (const [op, line] of diffLines(st.old || "", st.new || "")) {
        pre.append("span").attr("class", "rp-dl rp-dl-" + (op === "+" ? "add" : op === "-" ? "del" : "ctx")).text(op + " " + line + "\n");
      }
    }
    if (st.out && !(st.diffs && st.diffs.length)) {
      const det = c.append("details").attr("class", "rp-out").property("open", st.k === "run" && st.fail);
      det.append("summary").text(st.fail ? "Output · failed" : "Output");
      det.append("pre").text(st.out);
    }
  }

  // diffLines shows an edit as the lines it removed and added, keeping the
  // lines both sides share at its start and end as context.
  function diffLines(a, b) {
    const A = a.split("\n"), B = b.split("\n");
    let p = 0;
    while (p < A.length && p < B.length && A[p] === B[p]) p++;
    let q = 0;
    while (q < A.length - p && q < B.length - p && A[A.length - 1 - q] === B[B.length - 1 - q]) q++;
    const out = [];
    for (const l of A.slice(Math.max(0, p - 2), p)) out.push([" ", l]);
    for (const l of A.slice(p, A.length - q)) out.push(["-", l]);
    for (const l of B.slice(p, B.length - q)) out.push(["+", l]);
    for (const l of A.slice(A.length - q, Math.min(A.length, A.length - q + 2))) out.push([" ", l]);
    return out;
  }

  // show moves everything to the playhead. from says who moved it, so the
  // mover is not moved back.
  function show(s, from) {
    const steps = s.r.steps, i = s.i, st = steps[i];
    s.where.text(st ? `Step ${i + 1} of ${steps.length} · ${clock(st.t0)}` + (s.r.live ? " · live" : "") : (s.r.live ? "Waiting for Claude…" : "No steps"));
    s.followBtn.attr("hidden", s.r.live && !s.follow ? null : true);
    s.listBox.selectAll("article.rp-card").classed("on", (d, j) => j === i);
    if (s.drawLanes) head(s, LANES.length * 22 + 22);
    if (from !== "list" && st) {
      const c = s.listBox.selectAll("article.rp-card").nodes()[i];
      if (c) {
        s.scrolling = true;
        s.listBox.node().scrollTop = c.offsetTop - s.listBox.node().offsetTop;
        requestAnimationFrame(() => { s.scrolling = false; });
      }
    }
    footprint(s);
  }

  // footprint colours what Claude has touched up to the playhead: read files
  // blue, edited files green with their edit count, older touches fainter.
  // The file at the playhead is outlined.
  function footprint(s) {
    const steps = s.r.steps.slice(0, s.i + 1);
    const last = new Map(), edits = new Map(), outside = new Set();
    // A step touches the file its tool named, the files its command names,
    // and the files the tree shows it changed.
    const touch = (j, file, path, edit) => {
      if (!file || !s.fileTile.has(file)) { if (edit && (file || path)) outside.add(file || path); return; }
      const prev = last.get(file) || { j: -1, edit: false };
      last.set(file, { j, edit: prev.edit || edit });
      if (edit) edits.set(file, (edits.get(file) || 0) + 1);
    };
    steps.forEach((st, j) => {
      const diffs = st.diffs || [];
      if (st.file || st.path) touch(j, st.file, st.path, st.k === "edit" && !diffs.length);
      for (const f of st.reads || []) touch(j, f, f, false);
      for (const ch of diffs) touch(j, ch.file, ch.path, true);
    });
    // Changed lines per declaration up to the playhead: the green deepens with
    // them, so the parts of a file that took the work stand out from the
    // rest of it, which keeps only a faint tint.
    const lines = new Map(), now = new Set();
    steps.forEach((st, j) => {
      for (const ch of st.diffs || []) {
        for (const [id, n] of Object.entries(ch.decls || {})) {
          const di = s.byId.get(id);
          if (di === undefined) continue;
          lines.set(di, (lines.get(di) || 0) + n);
          if (j === s.i) now.add(di);
        }
      }
    });
    const most = Math.max(1, ...lines.values());
    const tone = new Map();
    for (const [f, v] of last) {
      const age = s.i - v.j;
      const op = Math.max(0.35, 1 - age / Math.max(8, steps.length));
      for (const di of s.declsOf.get(f) || []) tone.set(di, { cls: v.edit ? "fp-file" : "fp-read", op: v.edit ? 1 : op });
    }
    for (const [di, n] of lines) tone.set(di, { cls: "fp-edit", op: 0.3 + 0.7 * Math.log1p(n) / Math.log1p(most) });
    s.rects
      .attr("class", (t, i) => "t-" + t[K] + (t[K] === "d" ? " " + (tone.has(i) ? tone.get(i).cls : "h0") + (now.has(i) ? " fp-now" : "") : ""))
      .attr("opacity", (t, i) => (tone.has(i) ? tone.get(i).op : null));
    const counted = [...edits].map(([f, n]) => [s.d.tiles[s.fileTile.get(f)], n]);
    s.counts.selectAll("text").data(counted).join("text").attr("class", "rp-count").text(([, n]) => n + "×");
    const cur = s.r.steps[s.i];
    const curFile = cur && [cur.file, ...((cur.diffs || []).map((c) => c.file)), ...(cur.reads || [])].find((f) => f && s.fileTile.has(f));
    const ct = curFile ? s.d.tiles[s.fileTile.get(curFile)] : null;
    s.cur.attr("display", ct ? null : "none");
    if (ct) s.cur.datum(ct).attr("x", ct[X]).attr("y", ct[Y]).attr("width", ct[W]).attr("height", ct[H]).raise();
    s.newFiles.text(outside.size ? "Outside the map: " + [...outside].join(", ") : "");
    rescale(s);
  }

  function rescale(s) {
    const ppu = (s.svg.node().clientWidth || s.w) / s.w * s.k;
    const u = (px) => px / ppu;
    s.cur.attr("stroke-width", u(3));
    placeBars(s.bars, u);
    s.counts.selectAll("text").attr("font-size", u(11))
      .attr("x", ([t]) => t[X] + t[W] - u(4)).attr("y", ([t]) => t[Y] + u(12));
    labels(s.labels, ppu);
  }
})();
