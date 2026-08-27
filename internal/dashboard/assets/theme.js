// The theme, applied before first paint — which is why this file loads
// blocking in the head rather than deferred with the rest. The stored mode is
// "dark" or "light"; anything else means follow the system. The applied theme
// lands on <html data-theme>, the mode on <html data-theme-mode> where the
// toggle's CSS label reads it. Storage can be refused; every miss falls back
// to the system scheme.
"use strict";

(() => {
  const KEY = "hark_theme";
  const system = window.matchMedia("(prefers-color-scheme: light)");

  const apply = () => {
    let mode = null;
    try {
      mode = window.localStorage.getItem(KEY);
    } catch {}
    if (mode !== "dark" && mode !== "light") mode = "system";
    const light = mode === "light" || (mode === "system" && system.matches);
    document.documentElement.dataset.theme = light ? "light" : "dark";
    document.documentElement.dataset.themeMode = mode;
  };

  apply();
  system.addEventListener("change", apply);

  // Delegated, so the button survives every body swap htmx performs.
  document.addEventListener("click", (event) => {
    if (!event.target.closest("[data-theme-toggle]")) return;
    const order = ["system", "dark", "light"];
    const current = document.documentElement.dataset.themeMode || "system";
    const next = order[(order.indexOf(current) + 1) % order.length];
    try {
      if (next === "system") {
        window.localStorage.removeItem(KEY);
      } else {
        window.localStorage.setItem(KEY, next);
      }
    } catch {}
    apply();
  });
})();
