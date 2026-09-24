export function attachmentReadability(session, connections, attachment) {
  const connection = (connections || []).find((value) => value.id === session?.connection_id);
  const capabilities = connection?.capabilities;
  if (!connection || !capabilities?.probed_at) return null;
  const handling = connection.attachment_handling || "auto";
  if (attachment?.sidecar) return null;
  if (attachment?.kind === "image" && handling === "extract")
    return "This image needs OCR before the connection can read it";
  if (attachment?.kind === "image" && handling === "auto" && capabilities.vision !== "reads images")
    return capabilities.vision === "accepts images but does not read them"
      ? "This image needs OCR · the connection accepts images but does not read them"
      : "This connection cannot read images · probe did not verify image reading";
  if (attachment?.kind === "pdf" && handling === "extract")
    return "This PDF needs extraction before the connection can read it";
  if (attachment?.kind === "pdf" && handling === "auto" && !capabilities.document_input)
    return "This connection cannot read PDFs · probe found no document input";
  // Item 2ch (v1.2.5): a PDF has two routes and the operator chose - sidecar by
  // default, inline only under the threshold. The chip NAMES the route rather
  // than leaving him to infer it from the size and the connection.
  if (attachment?.kind === "pdf")
    return attachment.bytes > inlineDocumentLimit(session)
      ? "This PDF goes by extracted text · over the inline limit"
      : "This PDF goes inline · under the inline limit";
  if (attachment?.kind === "binary")
    return "This connection cannot read this file type";
  return null;
}

// The install-wide threshold, as the state reports it, with the stated default.
export function inlineDocumentLimit(session) {
  const configured = Number(session?.attachment_inline_max_bytes || 0);
  return configured > 0 ? configured : 2 * 1024 * 1024;
}
