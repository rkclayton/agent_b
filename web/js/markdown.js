export function renderMarkdown(root, text) {
  const document = root.ownerDocument || globalThis.document;
  root.replaceChildren();
  const lines = String(text).split("\n");
  let index = 0;
  while (index < lines.length) {
    if (lines[index].startsWith("```")) {
      const code = [];
      index++;
      while (index < lines.length && !lines[index].startsWith("```")) code.push(lines[index++]);
      index++;
      const pre = document.createElement("pre");
      pre.textContent = code.join("\n");
      root.append(pre);
    } else if (/^[-*] /.test(lines[index])) {
      const list = document.createElement("ul");
      while (index < lines.length && /^[-*] /.test(lines[index])) {
        const item = document.createElement("li");
        inline(document, item, lines[index].slice(2));
        list.append(item);
        index++;
      }
      root.append(list);
    } else if (lines[index].trim()) {
      const paragraph = [];
      while (index < lines.length && lines[index].trim() && !lines[index].startsWith("```") && !/^[-*] /.test(lines[index])) paragraph.push(lines[index++]);
      const p = document.createElement("p");
      inline(document, p, paragraph.join("\n"));
      root.append(p);
    } else index++;
  }
}

function inline(document, root, text) {
  const pattern = /(`[^`]+`|\*\*[^*]+\*\*)/g;
  let at = 0;
  for (const match of text.matchAll(pattern)) {
    root.append(document.createTextNode(text.slice(at, match.index)));
    const value = match[0];
    const node = document.createElement(value.startsWith("`") ? "code" : "strong");
    node.textContent = value.startsWith("`") ? value.slice(1, -1) : value.slice(2, -2);
    root.append(node);
    at = match.index + value.length;
  }
  root.append(document.createTextNode(text.slice(at)));
}
