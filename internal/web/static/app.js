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
