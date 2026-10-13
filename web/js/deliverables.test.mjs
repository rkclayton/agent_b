import assert from "node:assert/strict";
import test from "node:test";

import { attachmentDisplay, createAttachmentBubble, createFileChip, filesFromResponse, probeFile } from "./deliverables.js";

class FakeNode {
  constructor(tagName) {
    this.tagName = tagName;
    this.children = [];
    this.className = "";
    this.disabled = false;
    this.textContent = "";
    this.attributes = {};
  }
  append(...nodes) { this.children.push(...nodes); }
  setAttribute(name, value) { this.attributes[name] = value; }
}

const document = { createElement: (tag) => new FakeNode(tag) };

test("attachment bubbles show pictures, pasted dimensions, types, and one remove action 2u1", () => {
  assert.deepEqual(attachmentDisplay({ kind: "image", display_name: "Pasted image", width: 1280, height: 720, path: "attachments/pasted.png" }), { name: "Pasted image · 1280 × 720", type: "PNG", image: true });
  assert.deepEqual(attachmentDisplay({ kind: "pdf", path: "attachments/store-floor-plan.pdf" }), { name: "store-floor-plan.pdf", type: "PDF", image: false });
  let removed = 0;
  const picture = createAttachmentBubble(document, { attachment: true, kind: "image", path: "attachments/view.png" }, { sessionID: "main", state: "sent as text", title: "full note", removable: true }, { remove() { removed++; } });
  assert.match(picture.className, /chat-bubble/);
  assert.equal(picture.children[0].tagName, "img");
  assert.equal(picture.children[0].src, "/api/files/attachments/view.png?session=main");
  assert.equal(picture.children[1].children[0].textContent, "view.png");
  assert.equal(picture.children[1].children[1].textContent, "sent as text");
  assert.equal(picture.title, "full note");
  assert.equal(picture.children[2].attributes["aria-label"], "Remove view.png");
  picture.children[2].onclick(); assert.equal(removed, 1);
  const pdf = createAttachmentBubble(document, { attachment: true, kind: "pdf", path: "attachments/plan.pdf" }, {}, {});
  assert.equal(pdf.children[0].textContent, "PDF");
});

test("file chips derive only successful file-producing tools from a canned projected response", () => {
  const items = [
    { type: "agent", run_id: "r7", toolCallIDs: ["write", "read", "script", "edit", "service"] },
    { type: "tool", callID: "write", name: "write_file", args: { path: "draft.txt" }, result: { ok: true, file: { path: "reports/final.txt", bytes: 1536 } } },
    { type: "tool", callID: "read", name: "read_file", args: { path: "input.txt" }, result: { ok: true } },
    { type: "tool", callID: "script", name: "run_script", args: { language: "python" }, result: { ok: true } },
    { type: "tool", callID: "edit", name: "edit_file", args: { path: "legacy.md" }, result: { ok: true } },
    { type: "tool", callID: "service", name: "call_service", args: { service: "depot", operation: "exportReport" }, result: { ok: true, file: { path: "depot-report.xlsx", bytes: 2048 } } },
  ];
  const files = filesFromResponse(items);
  assert.deepEqual(files, [
    { path: "reports/final.txt", bytes: 1536, callID: "write", runID: "r7", openScope: "workspace", openPath: "reports/final.txt" },
    { path: "legacy.md", bytes: null, callID: "edit", runID: "r7", openScope: "workspace", openPath: "legacy.md" },
    { path: "depot-report.xlsx", bytes: 2048, callID: "service", runID: "r7", openScope: "workspace", openPath: "depot-report.xlsx" },
  ]);
  const chip = createFileChip(document, files[0], { state: "ready", bytes: 1536 }, { openFolder() {} });
  assert.equal(chip.children[0].textContent, "final.txt");
  assert.equal(chip.children[1].textContent, "1.5 KiB");
  assert.equal(chip.children[2].tagName, "a");
  // Item 2ep: the folder link is a glyph with "folder" on hover, no visible word.
  assert.equal(chip.children[2].textContent, "");
  assert.equal(chip.children[2].title, "folder");
  assert.equal(chip.children.some((child) => child.textContent === "download" || child.textContent === "open folder" || child.textContent === "folder"), false);
  let opened = 0;
  const openable = createFileChip(document, { path: "reports/report.xlsx", bytes: 2534 }, { state: "ready", bytes: 2534 }, { openFolder() {}, openFile() { opened++; } });
  assert.equal(openable.children[0].className, "file-chip-name openable");
  openable.children[0].onclick();
  assert.equal(opened, 1);
  const script = createFileChip(document, { path: "run.bat", bytes: 9 }, { state: "ready", bytes: 9 }, { openFolder() {}, openFile() { opened++; } });
  assert.equal(script.children[0].className, "file-chip-name", "a file whose default action runs it is not opened from its name");
});

test("one image result exposes every returned picture as a file chip 2s3", () => {
  const files = filesFromResponse([
    { type: "agent", run_id: "run-images", toolCallIDs: ["pictures"] },
    { type: "tool", name: "image", callID: "pictures", result: { ok: true, files: [
      { path: "image-1.png", bytes: 12 },
      { path: "image-2.png", bytes: 15 },
    ] } },
  ]);
  assert.deepEqual(files.map(({ path, bytes, runID }) => ({ path, bytes, runID })), [
    { path: "image-1.png", bytes: 12, runID: "run-images" },
    { path: "image-2.png", bytes: 15, runID: "run-images" },
  ]);
});

test("both mode points open-folder at the durable exchange copy", () => {
  const items = [
    { type: "agent", run_id: "r8", toolCallIDs: ["write"] },
    { type: "tool", callID: "write", name: "write_file", args: { path: "reports/final.txt" }, result: { ok: true, file: { path: "reports/final.txt", bytes: 8 } } },
    { type: "notice", event: { type: "files.delivered", data: { mode: "both", items: [{ source_path: "reports/final.txt", exchange_path: "final (2).txt", status: "copied" }] } } },
  ];
  const [file] = filesFromResponse(items);
  assert.equal(file.openScope, "exchange");
  assert.equal(file.openPath, "final (2).txt");
});

test("a replay file absent from the workspace renders missing", async () => {
  const status = await probeFile("/api/files/gone.txt?session=main", async () => ({ ok: false, headers: new Map() }));
  const chip = createFileChip(document, { path: "gone.txt", bytes: 12 }, status, { openFolder() {} });
  assert.deepEqual(status, { state: "missing", bytes: null });
  assert.equal(chip.className, "file-chip missing");
  assert.equal(chip.children[1].textContent, "missing");
  assert.equal(chip.children.some((child) => child.textContent === "download"), false);
  assert.equal(chip.children.some((child) => child.textContent === "folder"), false);
});

test("a malformed write entry cannot abort deliverable discovery for its neighbors", () => {
  const malformed = new Proxy({ type: "tool", name: "write_file", result: { ok: true } }, {
    get(target, key) {
      if (key === "args") throw new Error("malformed projected args");
      return target[key];
    },
  });
  assert.deepEqual(filesFromResponse([
    malformed,
    { type: "tool", callID: "kept", name: "edit_file", args: { path: "kept.txt" }, result: { ok: true } },
  ]), [
    { path: "kept.txt", bytes: null, callID: "kept", runID: "", openScope: "workspace", openPath: "kept.txt" },
  ]);
});
