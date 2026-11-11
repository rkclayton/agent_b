import { menuLabels } from "./chat-list.js";
import { registerMenu, requestConfirmation, requestText } from "./menu-behavior.js";

export const filterChats = (rows, query) => {
  const needle = String(query || "").trim().toLocaleLowerCase();
  return needle ? rows.filter((row) => String(row.name || "").toLocaleLowerCase().includes(needle)) : rows;
};

export function virtualRange(scrollTop, viewport, total, rowHeight = 28, margin = 6) {
  const count = Math.min(total, Math.ceil(viewport / rowHeight) + margin);
  const start = Math.max(0, Math.min(Math.floor(scrollTop / rowHeight) - margin, total - count));
  return { start, end: start + count };
}

const element = (tag, className = "", text = "") => { const value = document.createElement(tag); value.className = className; value.textContent = text; return value; };
const newestFirst = (left, right) => Date.parse(right.last_activity || 0) - Date.parse(left.last_activity || 0) || String(left.id).localeCompare(String(right.id));

function activityLabel(value) {
  const at = Date.parse(value || 0), elapsed = Date.now() - at;
  if (!Number.isFinite(at)) return "";
  if (elapsed < 60 * 60 * 1000) return `${Math.max(1, Math.floor(elapsed / 60000))} min ago`;
  if (elapsed < 24 * 60 * 60 * 1000) return `${Math.floor(elapsed / 3600000)} h ago`;
  if (elapsed < 7 * 24 * 60 * 60 * 1000) return elapsed < 2 * 24 * 60 * 60 * 1000 ? "yesterday" : `${Math.floor(elapsed / 86400000)} days ago`;
  return new Date(at).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

export function createChatsWindow({ getTree, getSession, onTreeAction, onDelete, onRename, onOpen }) {
  const backdrop = element("div", "chats-window-backdrop"); backdrop.hidden = true;
  const sheet = element("section", "chats-window"); sheet.hidden = true; sheet.setAttribute("aria-label", "Chats"); sheet.setAttribute("aria-hidden", "true");
  const head = element("header", "chats-window-head"); const title = element("strong", "", "Chats"); const close = element("button", "chats-window-close", "×"); close.type = "button"; close.title = "Dismiss Chats"; close.setAttribute("aria-label","Close Chats"); head.append(title, close);
  const searchWrap = element("label", "chats-window-search"); const search = document.createElement("input"); search.type = "search"; search.placeholder = "Search"; search.setAttribute("aria-label", "Search chats"); searchWrap.append(search);
  const tabs = element("div", "chats-window-tabs"); const chatsTab = element("button", "selected"), archivedTab = element("button"); for (const tab of [chatsTab,archivedTab]) tab.type = "button"; tabs.append(chatsTab,archivedTab);
  const header = element("div", "chats-window-table-head"); header.innerHTML = '<span></span><span>Name</span><span>Folder</span><span>Last activity</span><span></span>';
  const list = element("div", "chats-window-list"); const rowsHost = element("div", "chats-window-rows"); list.append(rowsHost);
  const empty = element("div", "chats-window-empty", "No chats match"); empty.hidden = true;
  const bar = element("footer", "chats-window-bar"); bar.hidden = true; const count = element("span", "chats-window-selected"); const archive = element("button"), move = element("button", "", "Move to folder"), remove = element("button", "alarm", "Delete"); for (const button of [archive, move, remove]) button.type = "button"; bar.append(count, archive, move, remove);
  sheet.append(head, searchWrap, tabs, header, list, empty, bar); document.body.append(backdrop, sheet);
  let all = [], filtered = [], selected = new Set(), lastPicked = -1, errors = new Map(), open = false, lastFocus = null, folderMenu = null, archived = false;

  const read = () => [...(getTree()?.chats || [])].filter((row)=>!!row.archived===archived).sort(newestFirst);
  const updateBar = () => { count.textContent = `${selected.size} selected`; bar.hidden = selected.size === 0; sheet.classList.toggle("picking", selected.size > 0); };
  const refresh = () => { const tree=getTree()?.chats||[]; all = read(); filtered = filterChats(all, search.value); selected = new Set([...selected].filter((id) => all.some((row) => row.id === id))); chatsTab.textContent = `Chats ${tree.filter((row)=>!row.archived).length}`; archivedTab.textContent=`Archived ${tree.filter((row)=>row.archived).length}`; chatsTab.classList.toggle("selected",!archived); archivedTab.classList.toggle("selected",archived); archive.textContent=archived?"Restore":"Archive"; move.hidden=archived; empty.hidden = filtered.length !== 0; list.hidden = !filtered.length; renderRows(); updateBar(); };
  const runTree = async (body) => { try { errors.delete(body.id); await onTreeAction(body); refresh(); return true; } catch (error) { errors.set(body.id, error.message); refresh(); return false; } };
  const chooseFolder = (ids, anchor) => {
    folderMenu?.remove(); folderMenu = element("div", "shell-menu chats-window-folder-menu");
    for (const folder of ["", ...(getTree()?.folders || [])]) { const choice = element("button", "chat-list-menu-action", folder || "No folder"); choice.type = "button"; choice.onclick = async () => { const results = await Promise.allSettled(ids.map((id) => onTreeAction({ action:"move", id, folder }))); results.forEach((result, index) => { const id = ids[index]; if (result.status === "fulfilled") { errors.delete(id); selected.delete(id); } else errors.set(id, result.reason?.message || String(result.reason)); }); folderMenu.remove(); folderMenu = null; refresh(); }; folderMenu.append(choice); }
    sheet.append(folderMenu); registerMenu(folderMenu, { anchor }); folderMenu.hidden = false;
  };
  const rowMenu = (row, anchor, host) => {
    const menu = element("div", "shell-menu chat-list-row-menu"); menu.hidden = true; const meta = row, session = getSession(row.id) || {};
    if (row.archived) { const restore=element("button","chat-list-menu-action","Restore"), del=element("button","chat-list-menu-action alarm","Delete"); restore.type=del.type="button"; restore.onclick=()=>void runTree({action:"restore",id:row.id}); del.onclick=async()=>{if(await requestConfirmation("Delete this chat permanently? Memory notes it made are kept.",{anchor,confirmLabel:"Delete",danger:true}))try{await onDelete(row.id);refresh();}catch(error){errors.set(row.id,error.message);refresh();}}; menu.append(restore,del); host.append(menu); registerMenu(menu,{anchor}).open(); return; }
    const [pinLabel, renameLabel, moveLabel, archiveLabel, deleteLabel] = menuLabels(!!meta.pinned);
    const pin = element("button", "chat-list-menu-action", pinLabel); pin.onclick = () => void runTree({ action:"pin", id:row.id, pinned:!meta.pinned });
    const rename = element("button", "chat-list-menu-action", renameLabel); rename.onclick = async () => { const name = await requestText("Chat name", row.name, { anchor, confirmLabel:"Save" }); if (name) { try { await onRename(row.id, name); row.name = name; refresh(); } catch (error) { errors.set(row.id, error.message); refresh(); } } };
    const moveOne = element("button", "chat-list-menu-action", moveLabel); moveOne.dataset.menuReveal = ""; moveOne.onclick = () => chooseFolder([row.id], moveOne);
    const archiveOne=element("button","chat-list-menu-action",archiveLabel); archiveOne.onclick=()=>void runTree({action:"archive",id:row.id});
    const del = element("button", "chat-list-menu-action alarm", deleteLabel); del.onclick = async () => { if (!await requestConfirmation("Delete this chat permanently? Memory notes it made are kept.", { anchor, confirmLabel:"Delete", danger:true })) return; try { await onDelete(row.id); refresh(); } catch (error) { errors.set(row.id, error.message); refresh(); } };
    for (const button of [pin, rename, moveOne, archiveOne, del]) button.type = "button"; menu.append(pin, rename, moveOne); if(!["running","queued","stopping","paused"].includes(session.run?.status)&&!session.pending_approval&&!session.pending_repo_policy)menu.append(archiveOne); menu.append(del); host.append(menu); const controller = registerMenu(menu, { anchor }); controller.open();
  };
  function renderRows() {
    if (!open || !filtered.length) { rowsHost.replaceChildren(); return; }
    const range = virtualRange(list.scrollTop, list.clientHeight || 360, filtered.length), fragment = document.createDocumentFragment();
    const top = element("div", "chats-window-spacer"); top.style.height = `${range.start * 28}px`; fragment.append(top);
    for (let index=range.start; index<range.end; index++) { const row=filtered[index], line=element("div", "chats-window-row"); line.dataset.session=row.id; line.setAttribute("role", "row");
      const check=document.createElement("input"); check.type="checkbox"; check.setAttribute("aria-label", `Select ${row.name}`); check.checked=selected.has(row.id); check.onclick=(event)=>{ const next=check.checked; if(event.shiftKey&&lastPicked>=0){for(let at=Math.min(lastPicked,index);at<=Math.max(lastPicked,index);at++) next?selected.add(filtered[at].id):selected.delete(filtered[at].id);}else next?selected.add(row.id):selected.delete(row.id); lastPicked=index; renderRows(); updateBar(); };
      const name=element("button", "chats-window-name", row.name); name.type="button"; name.onclick=()=>{ closeWindow(); onOpen(row.id); };
      const folder=element("span", "chats-window-folder", row.folder || ""); const activity=element("span", "chats-window-activity", activityLabel(row.last_activity));
      const more=element("button", "chats-window-more", "⋮"); more.type="button"; more.setAttribute("aria-label", `${row.name} menu`); more.onclick=(event)=>{event.stopPropagation();rowMenu(row,more,line);}; line.append(check,name,folder,activity,more);
      if(errors.has(row.id)) line.append(element("div", "chats-window-row-error", errors.get(row.id))); fragment.append(line); }
    const bottom=element("div", "chats-window-spacer"); bottom.style.height=`${(filtered.length-range.end)*28}px`; fragment.append(bottom); rowsHost.replaceChildren(fragment);
  }
  const closeWindow = () => { if (!open) return; open=false; sheet.hidden=true; backdrop.hidden=true; sheet.setAttribute("aria-hidden","true"); document.body.classList.remove("chats-window-open"); folderMenu?.remove(); folderMenu=null; lastFocus?.focus(); };
  const openWindow = () => { lastFocus=document.activeElement; open=true; archived=false; selected.clear(); errors.clear(); search.value=""; list.scrollTop=0; sheet.hidden=false; backdrop.hidden=false; sheet.setAttribute("aria-hidden","false"); document.body.classList.add("chats-window-open"); refresh(); requestAnimationFrame(()=>search.focus({preventScroll:true})); };
  const selectTab=(value)=>{archived=value;selected.clear();lastPicked=-1;list.scrollTop=0;refresh();}; chatsTab.onclick=()=>selectTab(false); archivedTab.onclick=()=>selectTab(true);
  search.oninput=()=>{list.scrollTop=0;selected.clear();lastPicked=-1;refresh();}; list.onscroll=renderRows; close.onclick=closeWindow; backdrop.onclick=closeWindow;
  archive.onclick=async()=>{const action=archived?"restore":"archive",ids=[...selected],results=await Promise.allSettled(ids.map((id)=>onTreeAction({action,id})));results.forEach((result,index)=>{const id=ids[index];if(result.status==="fulfilled")selected.delete(id);else errors.set(id,result.reason?.message||String(result.reason));});refresh();}; move.onclick=()=>chooseFolder([...selected],move); remove.onclick=async()=>{ const ids=[...selected]; if(!await requestConfirmation(`Delete ${ids.length} chats? Memory notes, plans and files stay.`,{anchor:remove,confirmLabel:"Delete",danger:true}))return; const results=await Promise.allSettled(ids.map((id)=>onDelete(id))); results.forEach((result,index)=>{const id=ids[index];if(result.status==="fulfilled")selected.delete(id);else errors.set(id,result.reason?.message||String(result.reason));}); refresh(); };
  document.addEventListener("keydown",(event)=>{if((event.ctrlKey||event.metaKey)&&event.key.toLowerCase()==="k"){event.preventDefault();openWindow();return;}if(open&&event.key==="Escape"){event.preventDefault();closeWindow();}});
  return { open:openWindow, close:closeWindow, refresh };
}
