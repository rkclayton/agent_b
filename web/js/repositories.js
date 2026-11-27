import { api, reduce, setSelection, store } from "./bus.js";

export function mountRepositories(panel, close) {
  if (!panel || panel.dataset.mounted) return;
  panel.dataset.mounted = "true";
  const search = panel.querySelector("input");
  const add = panel.querySelector("[data-repository-add]");
  const list = panel.querySelector("tbody");
  const error = panel.querySelector("[role=status]");
  let repos = [];
  const render = () => {
    const needle = search.value.trim().toLowerCase();
    const shown = repos.filter(repo => repo.repo.toLowerCase().includes(needle));
    list.replaceChildren(...shown.map(repo => {
      const row = document.createElement("tr"), cell = row.insertCell();
      const open = document.createElement("button");
      open.type = "button";
      open.textContent = repo.repo;
      open.disabled = Boolean(repo.missing || store.replay);
      open.onclick = async () => {
        open.disabled = true;
        try {
          const { session } = await api("/api/sessions", { repository: repo.repo });
          reduce({ type: "snapshot", data: { sessions: { ...store.sessions, [session.id]: session } } });
          setSelection("agent_b", session.id);
          close();
        } catch (failure) {
          error.textContent = failure.message;
          open.disabled = false;
        }
      };
      cell.append(open);
      return row;
    }));
    panel.querySelector("[data-repository-empty]").hidden = shown.length > 0;
  };
  const load = async () => {
    repos = await api("/api/repositories", undefined, "GET");
    render();
  };
  search.oninput = render;
  add.disabled = Boolean(store.replay);
  add.onclick = async () => {
    add.disabled = true;
    error.textContent = "";
    try {
      const { path } = await api("/api/folder-picker", {});
      if (path) {
        await api("/api/repositories", { path });
        await load();
      }
    } catch (failure) {
      error.textContent = failure.message;
    } finally {
      add.disabled = false;
    }
  };
  void load().catch(failure => { error.textContent = failure.message; });
}
