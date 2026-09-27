// Item 2mf (a) and (b): A TAB POINTS AT A SURFACE, NOT AT A SESSION.
//
// The strip was hard-wired to chats — every wrap carried a session id, every tab
// was named by the session and lit by its run state, and selection WAS a session
// id, so no tab could point at anything that was not a conversation. A surface is
// a kind and a key. The chat kind carries a session; every other kind carries
// whatever identifies it, and nothing forces it to invent one.
//
// The operator's direction is why this is architecture and not a feature: "it
// should be treated as a similar type of window, so later we can do separate
// windows if we want or keep them all together." A surface already has a URL, so
// a surface can already be opened in a window. This module is where that list
// lives; the strip renders it and knows nothing else about what a kind is.
//
// (h): PLAN IS THE FIRST USER, NOT THE ONLY ONE. No other surface moves in this
// item — Settings and the activity page stay exactly where they are. Making
// everything a tab at once is how a refactor becomes a rewrite.

export const CHAT_KIND = "chat";
export const PLAN_KIND = "plan";

// A surface's key is what identifies it inside its kind. A chat's key is its
// session id; a static surface's key is its own name, because there is only one.
export const PLAN_SURFACE = Object.freeze({ kind: PLAN_KIND, key: PLAN_KIND });

export function chatSurface(session) {
  return { kind: CHAT_KIND, key: String(session?.id || "") };
}

export function sameSurface(a, b) {
  return !!a && !!b && a.kind === b.kind && a.key === b.key;
}

// The static surfaces, in the order they are pinned, AFTER every chat. Plan does
// not sort with the chats and does not move when one opens, closes or reorders,
// because it is not in the list that sorting touches.
export const STATIC_SURFACES = Object.freeze([PLAN_SURFACE]);

const STATIC_DETAIL = {
  [PLAN_KIND]: { label: "plan", title: "Plan", href: "/plan", page: "plan" },
};

export function surfaceLabel(surface) {
  return STATIC_DETAIL[surface?.kind]?.label || "";
}

export function surfaceTitle(surface) {
  return STATIC_DETAIL[surface?.kind]?.title || "";
}

export function surfaceHref(surface, sessionID = "") {
  if (surface?.kind === CHAT_KIND) {
    const key = surface.key || sessionID;
    return key ? `/chat?session=${encodeURIComponent(key)}` : "/chat";
  }
  const detail = STATIC_DETAIL[surface?.kind];
  if (!detail) return "";
  const suffix = sessionID ? `?session=${encodeURIComponent(sessionID)}` : "";
  return `${detail.href}${suffix}`;
}

// The page name the shell uses for `page`, so a document that loaded on a
// surface can tell which tab is selected without a second table.
export function surfacePage(surface) {
  return surface?.kind === CHAT_KIND ? CHAT_KIND : STATIC_DETAIL[surface?.kind]?.page || "";
}

export function surfaceForPage(page, sessionID = "") {
  if (page === CHAT_KIND) return { kind: CHAT_KIND, key: sessionID };
  const kind = Object.keys(STATIC_DETAIL).find((name) => STATIC_DETAIL[name].page === page);
  return kind ? { kind, key: kind } : null;
}

// (d), (e) and (f): A HIDDEN SURFACE IS HIDDEN, NOT GONE. The config carries the
// names of the kinds the operator has hidden and nothing else; the surface itself,
// its URL and its page are untouched, so re-enabling restores it where it was —
// pinned at the far right, because that is where the list puts it.
//
// Only a static surface may be hidden. A chat is closed, which is [[2hq]]'s
// close-is-not-delete and a different thing entirely.
export const HIDEABLE_KINDS = Object.freeze([PLAN_KIND]);

export function canHide(surface) {
  return HIDEABLE_KINDS.includes(surface?.kind);
}

export function hiddenKinds(config) {
  const raw = config?.chat?.hidden_surfaces;
  if (!Array.isArray(raw)) return new Set();
  return new Set(raw.filter((name) => HIDEABLE_KINDS.includes(name)));
}

export function isHidden(config, surface) {
  return hiddenKinds(config).has(surface?.kind);
}

export function withHidden(config, surface, hidden) {
  const names = hiddenKinds(config);
  if (hidden) names.add(surface.kind);
  else names.delete(surface.kind);
  // Sorted so the saved value does not depend on the order things were hidden in.
  return [...names].sort();
}

// The surfaces the strip draws: every chat it was already drawing, then the
// static ones the operator has not hidden.
export function visibleStaticSurfaces(config) {
  return STATIC_SURFACES.filter((surface) => !isHidden(config, surface));
}
