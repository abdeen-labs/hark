// The dashboard's whole client. Two behaviours that HTML alone cannot express:
// a confirmation before a destructive submit, and copying a one-time secret.
// Everything else is a form post and a server-rendered page.
"use strict";

// Destructive forms carry data-confirm. Delegated from the document so a form
// rendered on any page is covered without wiring anything up.
document.addEventListener("submit", (event) => {
  const message = event.target.dataset ? event.target.dataset.confirm : null;
  if (message && !window.confirm(message)) {
    event.preventDefault();
  }
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
