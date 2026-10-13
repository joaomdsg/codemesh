// Review keys: j/k move between units, r toggles the focused unit's mark and
// moves on, o shows or hides its diff, c opens the callers of the unit (or of
// the hop in focus), n writes a note on its first change (or on the hop's
// call line), t keeps the thread, b opens the basket, ? lists the keys.
const cm = (window.codemesh = window.codemesh || {});

// The reading page tells the browser its address when the thread changes.
cm.url = (u) => {
  if (u && u !== location.pathname + location.search) history.replaceState(null, "", u);
};

const click = (el) => {
  if (!el) return false;
  el.click();
  return true;
};

document.addEventListener("keydown", (e) => {
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  const help = document.querySelector(".keys-help");
  if (e.target.closest(".note-text")) {
    // The note box is a form without a form: Enter saves, Escape drops it.
    const form = e.target.closest(".note-form");
    if (e.key === "Enter" && click(form.querySelector(".note-save"))) e.preventDefault();
    if (e.key === "Escape" && click(form.querySelector(".note-cancel"))) e.preventDefault();
    return;
  }
  if (e.key === "Escape" && help) help.hidden = true;
  if (e.target.closest("input, textarea, select")) return;
  if (e.key === "?" && help) {
    help.hidden = !help.hidden;
    e.preventDefault();
    return;
  }
  if (e.key === "b") {
    const basket = document.querySelector(".tab.basket");
    if (basket) location.href = basket.href;
    return;
  }
  if (e.key === "t") {
    if (click(document.querySelector(".keep-thread"))) e.preventDefault();
    return;
  }
  const units = [...document.querySelectorAll(".unit")];
  if (units.length === 0) return;
  const here = document.activeElement;
  const at = units.indexOf(here && here.closest(".unit"));
  const hop = here && here.closest(".hop");
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
    case "c": {
      const toggle = hop ? hop.querySelector(".hop-follow") : at >= 0 && units[at].querySelector(".calls-toggle");
      if (!click(toggle)) return;
      break;
    }
    case "n": {
      const line = hop
        ? hop.querySelector(".dl-call .no[data-ln]")
        : at >= 0 && (units[at].querySelector(".dl-add .no[data-ln]") || units[at].querySelector(".dl-del .no[data-ln]"));
      if (!click(line)) return;
      break;
    }
    default: return;
  }
  e.preventDefault();
});

// The header's "? keys" does what the key does.
document.addEventListener("click", (e) => {
  const open = e.target.closest && e.target.closest(".keys-open");
  const help = document.querySelector(".keys-help");
  if (open && help) help.hidden = !help.hidden;
});

// A line number starts a note on that line: the browser names the line in a
// signal, then presses the button that posts it.
document.addEventListener("click", (e) => {
  const no = e.target.closest && e.target.closest(".no[data-ln]");
  const input = document.querySelector(".pick-input");
  if (!no || !input) return;
  input.value = no.dataset.ln;
  input.dispatchEvent(new Event("input", { bubbles: true }));
  const go = document.querySelector(".pick-go");
  if (go) go.click();
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

// A note box that has just opened takes the keyboard.
let noted = null;
const focusNote = () => {
  const box = document.querySelector(".note-form .note-text");
  if (box && box !== noted) box.focus();
  noted = box;
};
document.addEventListener("via:patch", () => queueMicrotask(() => { highlight(); focusNote(); }));

document.addEventListener("DOMContentLoaded", () => {
  const threaded = document.querySelector(".unit.threaded");
  if (threaded) threaded.scrollIntoView({ block: "start" });
});
