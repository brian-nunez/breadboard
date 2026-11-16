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
