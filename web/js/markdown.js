export function renderMarkdown(root, text) {
  const document = root.ownerDocument || globalThis.document;
  root.replaceChildren();
  const lines = String(text).split("\n");
  let index = 0;
  while (index < lines.length) {
    if (lines[index].trimStart().startsWith("```")) {
      const parsed = fencedBlock(document, lines, index, lines[index].search(/\S/));
      root.append(parsed.node);
      index = parsed.next;
    } else if (listLine(lines[index])) {
      const ordered = /^\d+\./.test(listLine(lines[index]).marker);
      const list = document.createElement(ordered ? "ol" : "ul");
      while (index < lines.length) {
        const match = listLine(lines[index]);
        if (!match || /^\d+\./.test(match.marker) !== ordered) break;
        const item = document.createElement("li");
        inline(document, item, match.text);
        index++;
        while (index < lines.length && lines[index].trimStart().startsWith("```") && lines[index].search(/\S/) > match.indent) {
          const parsed = fencedBlock(document, lines, index, lines[index].search(/\S/));
          item.append(parsed.node);
          index = parsed.next;
        }
        list.append(item);
      }
      root.append(list);
    } else if (headingLine(lines[index])) {
      const heading = headingLine(lines[index]);
      const node = document.createElement(`h${heading.level}`);
      node.className = "chat-markdown-heading";
      inline(document, node, heading.text);
      root.append(node);
      index++;
    } else if (tableAt(lines, index)) {
      const parsed = tableBlock(document, lines, index);
      root.append(parsed.node);
      index = parsed.next;
    } else if (ruleLine(lines[index])) {
      root.append(document.createElement("hr"));
      index++;
    } else if (lines[index].trim()) {
      const paragraph = [];
      while (index < lines.length && lines[index].trim() && !lines[index].trimStart().startsWith("```") && !listLine(lines[index]) && !headingLine(lines[index]) && !tableAt(lines, index) && !ruleLine(lines[index])) paragraph.push(lines[index++]);
      const p = document.createElement("p");
      inline(document, p, paragraph.join("\n"));
      root.append(p);
    } else index++;
  }
}

function headingLine(line) {
  const match = /^\s{0,3}(#{1,4})\s+(.+?)\s*$/.exec(line);
  return match ? { level: match[1].length, text: match[2] } : null;
}

function ruleLine(line) {
  const value = line.trim();
  return /^(?:-{3,}|\*{3,}|_{3,})$/.test(value);
}

function tableCells(line) {
  let value = line.trim();
  if (!value.includes("|")) return null;
  if (value.startsWith("|")) value = value.slice(1);
  if (value.endsWith("|")) value = value.slice(0, -1);
  const cells = value.split("|").map((cell) => cell.trim());
  return cells.length > 1 ? cells : null;
}

function tableAt(lines, index) {
  const header = tableCells(lines[index] || "");
  const separator = tableCells(lines[index + 1] || "");
  return !!header && !!separator && header.length === separator.length && separator.every((cell) => /^:?-{3,}:?$/.test(cell));
}

function tableBlock(document, lines, start) {
  const headings = tableCells(lines[start]);
  const separators = tableCells(lines[start + 1]);
  const alignments = separators.map((cell) => cell.startsWith(":") && cell.endsWith(":") ? "center" : cell.endsWith(":") ? "right" : "left");
  const table = document.createElement("table");
  table.className = "chat-markdown-table";
  const thead = document.createElement("thead");
  const headingRow = document.createElement("tr");
  for (const [index, text] of headings.entries()) headingRow.append(tableCell(document, "th", text, alignments[index]));
  thead.append(headingRow);
  const tbody = document.createElement("tbody");
  let index = start + 2;
  while (index < lines.length) {
    const cells = tableCells(lines[index]);
    if (!cells) break;
    const row = document.createElement("tr");
    for (let column = 0; column < headings.length; column++) row.append(tableCell(document, "td", cells[column] || "", alignments[column]));
    tbody.append(row);
    index++;
  }
  table.append(thead, tbody);
  return { node: table, next: index };
}

function tableCell(document, tagName, text, alignment) {
  const cell = document.createElement(tagName);
  cell.style.textAlign = alignment;
  inline(document, cell, text);
  return cell;
}

function listLine(line) {
  const match = /^(\s*)([-*]|\d+\.)\s+(.*)$/.exec(line);
  return match ? { indent: match[1].length, marker: match[2], text: match[3] } : null;
}

function fencedBlock(document, lines, start, indent) {
  const code = [];
  // KEEP THE INFO STRING. The opening fence may carry a language
  // and it was thrown away, so a reader could not tell PowerShell from JSON
  // without reading the code. No highlighting in this item — the label, and the
  // hook for it.
  const info = lines[start].trimStart().replace(/^`+/, "").trim().split(/\s+/)[0] || "";
  let index = start + 1;
  while (index < lines.length && !lines[index].trimStart().startsWith("```")) {
    const line = lines[index++];
    code.push(line.slice(Math.min(indent, line.length)));
  }
  if (index < lines.length) index++;
  const wrapper = document.createElement("div");
  wrapper.className = "code-block";
  const button = document.createElement("button");
  button.type = "button";
  button.className = "code-copy";
 // the copy control STOPS BORROWING THE PAPERCLIP. The same glyph
  // meant "attach a file" in the composer and "copy this" here, which teaches a
  // reader that a glyph means whatever the surface feels like. Two overlapping
  // squares is the copy idiom and it is not used for anything else here.
  button.textContent = "⧉";
  button.ariaLabel = "Copy code";
  button.title = "Copy code";
  const value = code.join("\n");
  button.onclick = () => navigator.clipboard?.writeText(value);
  const pre = document.createElement("pre");
  pre.textContent = value;
  // (b) and (c): a labelled fence says what it is; an UNLABELLED one renders
  // exactly as it does today, because a local model will not always comply and
  // the reader must never be worse off than before the instruction existed.
  wrapper.append(button, pre);
  if (info) {
    // The hook for future highlighting is the attribute; the label is what a
    // reader sees. Appended AFTER the control so the block's first child stays
    // the copy button, which is what everything else reads it as.
    wrapper.setAttribute?.("data-language", info);
    const label = document.createElement("span");
    label.className = "code-language";
    label.textContent = info;
    wrapper.append(label);
  }
  return { node: wrapper, next: index };
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
