// The atlas island: D3 draws the module layout the server sends, zooms it,
// and links lit tiles to their review cards. A Datastar effect calls
// codemesh.atlas whenever the layout, the reviewed list or the focused card
// changes; the server owns every fact, the browser only the view.
(() => {
  const cm = (window.codemesh = window.codemesh || {});
  const K = 0, X = 1, Y = 2, W = 3, H = 4, NAME = 5, UNIT = 6;

  cm.atlas = (el, atlas, reviewed, focus) => {
    if (!window.d3 || !atlas || !atlas.tiles) return;
    let s = el.__atlas;
    if (!s) s = el.__atlas = mount(el, atlas);
    if (s.at !== atlas.at) {
      draw(s, atlas.tiles);
      s.at = atlas.at;
      s.focus = "";
    }
    const done = new Set(reviewed || []);
    s.decls
      .classed("open", (d) => d[UNIT] !== "" && !done.has(d[UNIT]))
      .classed("done", (d) => done.has(d[UNIT]))
      .classed("cur", (d) => d[UNIT] !== "" && d[UNIT] === focus);
    if (focus && focus !== s.focus) fly(s, atlas, focus);
    s.focus = focus;
  };

  function mount(el, atlas) {
    const svg = d3.select(el).append("svg")
      .attr("class", "atlas-svg")
      .attr("viewBox", `0 0 ${atlas.w} ${atlas.h}`)
      .attr("role", "img")
      .attr("aria-label", "Map of the module. Lit tiles are declarations in this change; click one to open its card.");
    const g = svg.append("g");
    const s = { svg, g, at: null, focus: "", decls: d3.select(null), labels: null, k: 1, w: atlas.w };
    s.zoom = d3.zoom()
      .scaleExtent([1, 60])
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

  function draw(s, tiles) {
    s.g.selectAll("*").remove();
    const rects = s.g.selectAll("rect").data(tiles).join("rect")
      .attr("class", (d) => "t-" + d[K] + (d[UNIT] ? " lit" : ""))
      .attr("x", (d) => d[X]).attr("y", (d) => d[Y])
      .attr("width", (d) => d[W]).attr("height", (d) => d[H]);
    rects.append("title").text((d) => d[NAME]);
    rects.filter((d) => d[UNIT]).on("click", (e, d) => {
      const card = document.getElementById(d[UNIT]);
      if (!card) return;
      card.focus({ preventScroll: true });
      card.scrollIntoView({ block: "start" });
    });
    s.decls = rects.filter((d) => d[K] === "d");
    // Packages, files and the lit declarations get labels; the rest would be
    // thousands of nodes nobody can read at map scale.
    s.labels = s.g.selectAll("text")
      .data(tiles.filter((d) => d[K] !== "d" || d[UNIT]))
      .join("text")
      .attr("class", (d) => "atlas-label l-" + d[K])
      .text((d) => d[NAME]);
    label(s);
  }

  // label sizes text to 11 screen pixels at any zoom and shows only labels
  // that fit their tile on screen.
  function label(s) {
    if (!s.labels) return;
    // Screen pixels per layout unit: the viewBox is scaled into the panel.
    const ppu = (s.svg.node().clientWidth || s.w) / s.w * s.k;
    const px = 11 / ppu;
    s.labels
      .attr("font-size", px)
      .attr("x", (d) => d[X] + 3 / ppu)
      .attr("y", (d) => d[Y] + px + 2 / ppu)
      .attr("display", (d) => {
        const w = d[W] * ppu, h = d[H] * ppu;
        return w > d[NAME].length * 7 + 6 && h > 16 ? null : "none";
      });
  }

  // fly frames the focused declaration's file with a margin, so the
  // declaration is read among its neighbours.
  function fly(s, atlas, focus) {
    const i = atlas.tiles.findIndex((d) => d[UNIT] === focus);
    if (i < 0) return;
    // Tiles come package, files, declarations: the file is the last "f" before.
    let f = atlas.tiles[i];
    for (let j = i; j >= 0; j--) {
      if (atlas.tiles[j][K] === "f") { f = atlas.tiles[j]; break; }
    }
    const k = Math.max(1, Math.min(60, (atlas.w * 0.8) / f[W], (atlas.h * 0.8) / f[H]));
    const to = d3.zoomIdentity
      .translate(atlas.w / 2, atlas.h / 2)
      .scale(k)
      .translate(-(f[X] + f[W] / 2), -(f[Y] + f[H] / 2));
    const still = matchMedia("(prefers-reduced-motion: reduce)").matches;
    (still ? s.svg : s.svg.transition().duration(450)).call(s.zoom.transform, to);
  }
})();
