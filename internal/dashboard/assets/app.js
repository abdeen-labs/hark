// The dashboard's whole client. Four behaviours that HTML alone cannot
// express: a confirmation before a destructive submit, copying a one-time
// secret, keeping the overview current without a reload, and keeping "3m ago"
// true while a page sits open. Everything else is a form post and a
// server-rendered page.
"use strict";

// Destructive forms carry data-confirm, answered by the layout's <dialog>
// instead of window.confirm. Delegated from the document so a form rendered on
// any page — or swapped in by the live poll — is covered without wiring
// anything up. With scripts off the form submits unconfirmed, which is the
// same trade the old confirm() made.
const confirmDialog = document.querySelector("[data-confirm-dialog]");
document.addEventListener("submit", (event) => {
  const form = event.target;
  const message = form.dataset ? form.dataset.confirm : null;
  if (!message || !confirmDialog) return;

  if (form.dataset.confirmed) {
    delete form.dataset.confirmed;
    return;
  }
  event.preventDefault();

  confirmDialog.querySelector("[data-confirm-message]").textContent = message;
  confirmDialog.returnValue = "";
  confirmDialog.showModal();
  confirmDialog.addEventListener("close", () => {
    if (confirmDialog.returnValue === "confirm") {
      form.dataset.confirmed = "true";
      form.requestSubmit();
    }
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
// hidden tab does not poll at all; coming back refreshes at once.
const live = document.querySelector("[data-live]");
if (live) {
  let etag = "";
  const refresh = async () => {
    if (document.hidden) return;
    let res;
    try {
      res = await fetch(live.dataset.live, {
        headers: etag ? { "If-None-Match": etag } : {},
      });
    } catch {
      return; // Transient network trouble; the next tick tries again.
    }
    // A redirect means the session expired mid-poll. Navigate rather than
    // keep polling a sign-in page: the reload lands on the form.
    if (res.redirected) {
      window.location.reload();
      return;
    }
    if (!res.ok) return;
    etag = res.headers.get("ETag") || "";
    // The body is this origin's own html/template output — the same escaped
    // markup a reload would draw — so assigning it is as safe as the page.
    live.innerHTML = await res.text();
  };
  window.setInterval(refresh, 5000);
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) refresh();
  });
}

// Relative times tick rather than rot. The vocabulary mirrors the server's
// formatAgo exactly — the swap above re-renders the same strings, and the two
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
