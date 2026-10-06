import assert from "node:assert/strict";
import test from "node:test";

import { attachmentReadability } from "./attachment-readability.js";

const session = { connection_id: "text-only" };
const connections = [{ id: "text-only", reads_images: false, capabilities: { probed_at: "2026-09-12T00:00:00Z", image_input: false, vision: "rejected", document_input: false } }];

test("marks an image unreadable for the active probed connection", () => {
  assert.equal(attachmentReadability(session, connections, { kind: "image" }), "This connection cannot read images");
});

test("keeps readable kinds and extracted PDFs unmarked", () => {
  assert.equal(attachmentReadability(session, connections, { kind: "text" }), null);
  assert.equal(attachmentReadability(session, connections, { kind: "pdf", sidecar: "attachments/report.pdf.txt" }), null);
});

test("keeps OCR sidecars and the reads-images switch readable", () => {
  assert.equal(attachmentReadability(session, connections, { kind: "image", sidecar: "attachments/screen.png.txt" }), null);
  const native = [{ ...connections[0], attachment_handling: "native", reads_images: true }];
  assert.equal(attachmentReadability(session, native, { kind: "image" }), null);
});

test("marks an extract override pending until its sidecar exists", () => {
  const extract = [{ ...connections[0], attachment_handling: "extract", capabilities: { ...connections[0].capabilities, image_input: true } }];
  assert.equal(attachmentReadability(session, extract, { kind: "image" }), "This image needs OCR before the connection can read it");
});

test("the explicit switch does not wait for a probe", () => {
  assert.equal(attachmentReadability(session, [{ id: "text-only", reads_images: false, capabilities: {} }], { kind: "image" }), "This connection cannot read images");
});

test("uses the currently selected session connection", () => {
  const capable = [...connections, { id: "vision", reads_images: true, capabilities: { probed_at: "2026-09-12T00:00:00Z", image_input: true, vision: "reads images", document_input: true } }];
  assert.equal(attachmentReadability({ connection_id: "vision" }, capable, { kind: "image" }), null);
});

test("routes an image to OCR whenever the switch is off", () => {
  const tolerant = [{ ...connections[0], capabilities: { ...connections[0].capabilities, image_input: true, vision: "accepts images but does not read them" } }];
  assert.equal(attachmentReadability(session, tolerant, { kind: "image" }), "This connection cannot read images");
});
