export function surfaceForPage(page, sessionID = "") {
  return page === "chat" ? { kind: "chat", key: sessionID } : null;
}
