const recentFirst = (left, right) => Date.parse(right.last_activity || 0) - Date.parse(left.last_activity || 0);

export function arrangeChats(sessions = {}, tree = {}) {
  const metadata = new Map((tree.chats || []).map((chat) => [chat.id, chat]));
  const chats = Object.values(sessions).filter((chat) => chat && (chat.role === "b" || chat.role === "d"));
  const pinned = chats.filter((chat) => metadata.get(chat.id)?.pinned).sort(recentFirst);
  const unpinned = chats.filter((chat) => !metadata.get(chat.id)?.pinned);
  const folders = [...(tree.folders || [])].sort().map((path) => ({
    path,
    chats: unpinned.filter((chat) => metadata.get(chat.id)?.folder === path).sort(recentFirst),
  }));
  const root = unpinned.filter((chat) => !metadata.get(chat.id)?.folder).sort(recentFirst);
  return { pinned, folders, root };
}

export const menuLabels = (pinned) => [pinned ? "Unpin" : "Pin", "Rename", "Move to folder", "Delete"];

export function panelDrag(state, delta, available) {
  if (state.hidden) return delta > 0 ? { width: Math.min(delta, Math.max(0, available - 128)), hidden: false } : state;
  const next = Math.max(0, Math.min(state.width + delta, Math.max(0, available - 128)));
  return { width: next <= 4 ? 0 : next, hidden: next <= 4 };
}
