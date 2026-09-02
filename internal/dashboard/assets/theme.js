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
    const meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.content = light ? "#f0f3fa" : "#0a0f1c";
  };

  apply();
  system.addEventListener("change", apply);

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
