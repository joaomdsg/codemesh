// Review keys: j/k move between units, r toggles the focused unit's mark and
// moves on, n jumps to the next unreviewed unit, o shows or hides its diff.
document.addEventListener("keydown", (e) => {
  if (e.metaKey || e.ctrlKey || e.altKey || e.target.closest("input, textarea, select")) return;
  const units = [...document.querySelectorAll(".unit")];
  if (units.length === 0) return;
  const at = units.indexOf(document.activeElement && document.activeElement.closest(".unit"));
  const go = (i) => {
    const u = units[i];
    if (!u) return;
    u.focus({ preventScroll: true });
    u.scrollIntoView({ block: "start" });
  };
  const nextOpen = (from) => units.findIndex((u, i) => i > from && !u.classList.contains("reviewed"));
  switch (e.key) {
    case "j": go(Math.min(at + 1, units.length - 1)); break;
    case "k": go(Math.max(at - 1, 0)); break;
    case "n": go(nextOpen(at)); break;
    case "o": {
      const open = at >= 0 && units[at].querySelector(".expand");
      if (!open) return;
      open.click();
      break;
    }
    case "r": {
      const mark = at >= 0 && units[at].querySelector(".mark");
      if (!mark) return;
      mark.click();
      go(nextOpen(at));
      break;
    }
    default: return;
  }
  e.preventDefault();
});

// The outline follows the focused unit. A live re-render replaces the
// outline, so the mark is put back after every patch.
let current = "";
const highlight = () => {
  document.querySelectorAll(".ol-row.cur").forEach((a) => a.classList.remove("cur"));
  const row = current && document.querySelector('.ol-row[href="#' + current + '"]');
  if (row) {
    row.classList.add("cur");
    row.scrollIntoView({ block: "nearest" });
  }
};
document.addEventListener("focusin", (e) => {
  const u = e.target.closest && e.target.closest(".unit");
  if (!u) return;
  current = u.id;
  highlight();
});
document.addEventListener("via:patch", () => queueMicrotask(highlight));
