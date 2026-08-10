// The dashboard's whole client, on top of a vendored htmx. hx-boost on the
// body does the navigation — every same-origin link and form becomes a fetch
// and a body swap — and this file adds the four behaviours that are ours: a
// confirmation before a destructive submit, copying a one-time secret, keeping
// the overview current without a reload, and keeping "3m ago" true while a
// page sits open. Everything in here is delegated or re-resolved per use, so
// content htmx swapped in five minutes ago is as covered as the first render.
"use strict";

// Destructive forms carry data-confirm, answered by the layout's <dialog>.
// htmx fires a cancellable htmx:confirm before every request it issues, which
// is the sanctioned seam for exactly this; intercepting submit directly would
// race the listener htmx installs on the form itself. With scripts off the
// form submits unconfirmed, which is the same trade window.confirm made.
document.addEventListener("htmx:confirm", (event) => {
  const source = event.detail.elt.closest("[data-confirm]");
  const dialog = document.querySelector("[data-confirm-dialog]");
  if (!source || !dialog) return;

  event.preventDefault();
  dialog.querySelector("[data-confirm-message]").textContent = source.dataset.confirm;
  dialog.returnValue = "";
  dialog.showModal();
  dialog.addEventListener("close", () => {
    // true: the request goes out as-is, with htmx's own confirm skipped —
    // this dialog was it.
    if (dialog.returnValue === "confirm") event.detail.issueRequest(true);
  }, { once: true });
});

document.addEventListener("click", async (event) => {
  const button = event.target.closest("[data-copy]");
  if (!button) return;

  try {
    await navigator.clipboard.writeText(button.dataset.copy);
  } catch {
    // Clipboard access can be refused, and there is nothing to fall back to:
    // the value is on screen and selectable.
    return;
  }

  const label = button.textContent;
  button.textContent = "Copied";
  button.disabled = true;
  window.setTimeout(() => {
    button.textContent = label;
    button.disabled = false;
  }, 1500);
});

// The overview polls for its own dynamic half. The server renders the same
// template block the page shipped with and answers If-None-Match with a 304,
// so a quiet account costs headers, and a delivery shows up within a poll. A
// hidden tab does not poll at all; coming back refreshes at once. The target
// is looked up every tick because boosted navigation replaces it: on any page
// without one, a tick is a no-op.
let liveETag = "";
const refreshLive = async () => {
  const live = document.querySelector("[data-live]");
  if (!live || document.hidden) return;
  let res;
  try {
    res = await fetch(live.dataset.live, {
      headers: liveETag ? { "If-None-Match": liveETag } : {},
    });
  } catch {
    return; // Transient network trouble; the next tick tries again.
  }
  // A redirect means the session expired mid-poll. Navigate rather than keep
  // polling a sign-in page: the reload lands on the form.
  if (res.redirected) {
    window.location.reload();
    return;
  }
  if (!res.ok) return;
  liveETag = res.headers.get("ETag") || "";
  // The body is this origin's own html/template output — the same escaped
  // markup a reload would draw — so assigning it is as safe as the page.
  live.innerHTML = await res.text();
};
window.setInterval(refreshLive, 5000);
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) refreshLive();
});

// Relative times tick rather than rot. The vocabulary mirrors the server's
// formatAgo exactly — the swaps above re-render the same strings, and the two
// must never disagree about what "3m ago" means.
const relative = (iso) => {
  const ms = Date.parse(iso) - Date.now();
  const s = Math.abs(ms) / 1000;
  if (s < 60) return "just now";
  const out = s < 3600 ? `${Math.floor(s / 60)}m`
    : s < 86400 ? `${Math.floor(s / 3600)}h`
    : `${Math.floor(s / 86400)}d`;
  return ms > 0 ? `in ${out}` : `${out} ago`;
};

window.setInterval(() => {
  for (const el of document.querySelectorAll("time[datetime]")) {
    if (el.getAttribute("datetime")) el.textContent = relative(el.getAttribute("datetime"));
  }
}, 30000);
