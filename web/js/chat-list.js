const recentFirst = (left, right) => Date.parse(right.last_activity || 0) - Date.parse(left.last_activity || 0);
const needsAttention = (chat) => !!(chat.pending_approval || chat.pending_repo_policy || ["running", "queued", "stopping", "paused"].includes(chat.run?.status));

export function arrangeChats(sessions = {}, tree = {}) {
  const metadata = new Map((tree.chats || []).map((chat) => [chat.id, chat]));
  const chats = Object.values(sessions).filter((chat) => chat && (chat.role === "b" || chat.role === "d") && !metadata.get(chat.id)?.archived);
  const pinned = chats.filter((chat) => metadata.get(chat.id)?.pinned).sort(recentFirst);
  const unpinned = chats.filter((chat) => !metadata.get(chat.id)?.pinned);
  const folders = [...(tree.folders || [])].sort().map((path) => ({
    path,
    chats: unpinned.filter((chat) => metadata.get(chat.id)?.folder === path).sort(recentFirst),
  }));
  const root = unpinned.filter((chat) => !metadata.get(chat.id)?.folder).sort(recentFirst);
  const recent = root.filter((chat, index) => index < 20 || needsAttention(chat));
  return { pinned, folders, root, recent };
}

export const menuLabels = (pinned) => [pinned ? "Unpin" : "Pin", "Rename", "Move to folder", "Archive", "Delete"];

export function panelDrag(state, delta, available) {
  if (state.hidden) return delta > 0 ? { width: Math.min(delta, Math.max(0, available - 128)), hidden: false } : state;
  const next = Math.max(0, Math.min(state.width + delta, Math.max(0, available - 128)));
  return { width: next <= 4 ? 0 : next, hidden: next <= 4 };
}
