// Breadboard UI: the only script. It copies commands and swaps in live
// updates; it never sends anything that changes the system.
"use strict";

document.addEventListener("click", (e) => {
  const button = e.target.closest("[data-copy]");
  if (!button) return;
  const block = button.closest("div").parentElement.querySelector("[data-copy-source]");
  if (!block) return;
  const text = Array.from(block.querySelectorAll("div"))
    .map((line) => line.textContent.replace(/^\$ /, ""))
    .join("\n");
  navigator.clipboard.writeText(text).then(() => {
    const label = button.textContent;
    button.textContent = "Copied";
    setTimeout(() => (button.textContent = label), 1200);
  });
});

// Elements with data-live="/path" are replaced by each "update" event the
// server streams from that path.
for (const el of document.querySelectorAll("[data-live]")) {
  const source = new EventSource(el.dataset.live);
  source.addEventListener("update", (e) => {
    el.innerHTML = e.data;
  });
}

// <pre data-logs="/path"> appends each "line" event, keeping the newest
// 2000 lines, and follows the end unless the reader scrolled up.
for (const pre of document.querySelectorAll("[data-logs]")) {
  const source = new EventSource(pre.dataset.logs);
  source.addEventListener("line", (e) => {
    const atEnd = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
    const line = document.createElement("div");
    line.textContent = e.data;
    pre.appendChild(line);
    while (pre.childElementCount > 2000) pre.firstElementChild.remove();
    if (atEnd) pre.scrollTop = pre.scrollHeight;
  });
}

// Elements with data-load="/path" are replaced once by that fragment.
for (const el of document.querySelectorAll("[data-load]")) {
  fetch(el.dataset.load, { credentials: "same-origin" })
    .then((r) => r.text())
    .then((html) => (el.innerHTML = html))
    .catch(() => (el.textContent = "Could not load. Reload the page to try again."));
}

// The search shortcut hint names the key this platform uses: ⌘K on Apple
// platforms, Ctrl K (the server's default) elsewhere. Both keys work
// everywhere.
(() => {
  const platform = navigator.userAgentData?.platform || navigator.platform || "";
  if (/mac|iphone|ipad|ipod/i.test(platform)) {
    for (const el of document.querySelectorAll("[data-search-shortcut]")) {
      el.textContent = "⌘K";
    }
  }
})();

// Search: ⌘K / Ctrl+K or any [data-search-open] opens the dialog. The index
// (title, kind, url for every page) is fetched once; filtering is local.
(() => {
  const dialog = document.getElementById("search");
  if (!dialog) return;
  const input = dialog.querySelector("[data-search-input]");
  const list = dialog.querySelector("[data-search-results]");
  let index = null;
  let selected = 0;
  let shown = [];

  const render = () => {
    const q = input.value.trim().toLowerCase();
    shown = (index || []).filter((e) => !q || (e.title + " " + e.kind).toLowerCase().includes(q)).slice(0, 30);
    selected = Math.min(selected, Math.max(shown.length - 1, 0));
    list.replaceChildren(
      ...shown.map((e, i) => {
        const li = document.createElement("li");
        const a = document.createElement("a");
        a.href = e.url;
        a.className = "flex items-center justify-between rounded-lg px-3 py-2 " + (i === selected ? "bg-indigo-50 text-indigo-700" : "text-slate-700 hover:bg-slate-50");
        const title = document.createElement("span");
        title.textContent = e.title;
        const kind = document.createElement("span");
        kind.className = "text-xs text-slate-400";
        kind.textContent = e.kind;
        a.append(title, kind);
        li.append(a);
        return li;
      })
    );
    if (!index || shown.length === 0) {
      const li = document.createElement("li");
      li.className = "px-3 py-2 text-slate-500";
      li.textContent = index ? "Nothing matches." : "Loading…";
      list.append(li);
    }
  };
  const open = () => {
    dialog.classList.remove("hidden");
    input.value = "";
    selected = 0;
    input.focus();
    if (!index) {
      fetch(dialog.dataset.searchIndex, { credentials: "same-origin" })
        .then((r) => r.json())
        .then((data) => {
          index = data;
          render();
        });
    }
    render();
  };
  const close = () => dialog.classList.add("hidden");

  document.addEventListener("click", (e) => {
    if (e.target.closest("[data-search-open]")) open();
    else if (e.target === dialog) close();
  });
  document.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
      e.preventDefault();
      dialog.classList.contains("hidden") ? open() : close();
    } else if (!dialog.classList.contains("hidden")) {
      if (e.key === "Escape") close();
      else if (e.key === "ArrowDown") { selected = Math.min(selected + 1, shown.length - 1); render(); e.preventDefault(); }
      else if (e.key === "ArrowUp") { selected = Math.max(selected - 1, 0); render(); e.preventDefault(); }
      else if (e.key === "Enter" && shown[selected]) window.location.href = shown[selected].url;
    }
  });
  input.addEventListener("input", () => { selected = 0; render(); });
})();
