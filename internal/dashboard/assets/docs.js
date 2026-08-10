"use strict";

const search = document.querySelector("[data-docs-search]");
const body = document.querySelector("[data-docs-body]");
const result = document.querySelector("[data-docs-result]");
const empty = document.querySelector("[data-docs-empty]");

// Turn each section and endpoint into a searchable unit without changing the
// source Markdown. A match keeps its parent section heading visible so the
// result never loses context.
if (search && body) {
  const headings = [...body.querySelectorAll("h2, h3")];
  const units = headings.map((heading) => {
    const nodes = [heading];
    let next = heading.nextElementSibling;
    while (next && !next.matches("h2, h3")) {
      nodes.push(next);
      next = next.nextElementSibling;
    }
    return {
      heading,
      nodes,
      text: nodes.map((node) => node.textContent).join(" ").toLocaleLowerCase(),
    };
  });
  const links = new Map(
    [...document.querySelectorAll(".toc a")].map((link) => [link.hash.slice(1), link.closest("li")]),
  );

  const filter = () => {
    const query = search.value.trim().toLocaleLowerCase();
    let matches = 0;

    for (const unit of units) {
      const visible = !query || unit.text.includes(query);
      unit.nodes.forEach((node) => { node.hidden = !visible; });
      const item = links.get(unit.heading.id);
      if (item) item.hidden = !visible;
      if (visible) matches += 1;
    }

    // A visible endpoint should retain the H2 that names its domain.
    for (const unit of units.filter((candidate) => candidate.heading.tagName === "H2")) {
      let next = unit.heading.nextElementSibling;
      let hasVisibleChild = false;
      while (next && next.tagName !== "H2") {
        if (next.tagName === "H3" && !next.hidden) hasVisibleChild = true;
        next = next.nextElementSibling;
      }
      if (hasVisibleChild) {
        unit.heading.hidden = false;
        const item = links.get(unit.heading.id);
        if (item) item.hidden = false;
      }
    }

    result.textContent = query ? `${matches} section${matches === 1 ? "" : "s"} found` : "";
    empty.hidden = !query || matches > 0;
  };

  search.addEventListener("input", filter);
  document.addEventListener("keydown", (event) => {
    if (event.key === "/" && !event.metaKey && !event.ctrlKey && !event.altKey &&
        !event.target.matches("input, textarea, select")) {
      event.preventDefault();
      search.focus();
    }
    if (event.key === "Escape" && document.activeElement === search) {
      search.value = "";
      filter();
      search.blur();
    }
  });
}

// The outline follows the reader. A heading crossing the band under the sticky
// topbar marks its own entry; the band's bottom margin keeps the mark on what
// is being read rather than on whatever just scrolled into the viewport's tail.
const toc = document.querySelector(".toc");
if (toc && body) {
  const linkFor = new Map(
    [...toc.querySelectorAll("a")].map((link) => [link.hash.slice(1), link]),
  );
  let current = null;
  const observer = new IntersectionObserver((entries) => {
    for (const entry of entries) {
      if (!entry.isIntersecting) continue;
      const link = linkFor.get(entry.target.id);
      if (!link || link === current) continue;
      if (current) current.removeAttribute("aria-current");
      link.setAttribute("aria-current", "true");
      current = link;
    }
  }, { rootMargin: "-72px 0px -75% 0px" });
  for (const heading of body.querySelectorAll("h2, h3")) observer.observe(heading);
}

// Examples are intentionally plain preformatted text in the source. Enhance
// them with a local copy control while leaving the document fully useful when
// scripts are disabled or clipboard permission is refused.
for (const block of document.querySelectorAll(".prose pre")) {
  const code = block.querySelector("code");
  const content = code ? code.textContent : block.textContent;
  const button = document.createElement("button");
  button.type = "button";
  button.className = "code-copy";
  button.textContent = "Copy";
  button.setAttribute("aria-label", "Copy code example");
  button.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(content);
    } catch {
      return;
    }
    button.textContent = "Copied";
    window.setTimeout(() => { button.textContent = "Copy"; }, 1500);
  });
  block.append(button);
}
