// Review keys: j/k move between units, r toggles the focused unit's mark and
// moves on, n jumps to the next unreviewed unit.
document.addEventListener("keydown", (e) => {
  if (e.metaKey || e.ctrlKey || e.altKey || e.target.closest("input, textarea, select")) return;
  const units = [...document.querySelectorAll(".unit")];
  if (units.length === 0) return;
  const at = units.indexOf(document.activeElement && document.activeElement.closest(".unit"));
  const go = (i) => {
    const u = units[i];
    if (!u) return;
    u.focus({ preventScroll: true });
    u.scrollIntoView({ block: "start", behavior: "smooth" });
  };
  const nextOpen = (from) => units.findIndex((u, i) => i > from && !u.classList.contains("reviewed"));
  switch (e.key) {
    case "j": go(Math.min(at + 1, units.length - 1)); break;
    case "k": go(Math.max(at - 1, 0)); break;
    case "n": go(nextOpen(at)); break;
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
