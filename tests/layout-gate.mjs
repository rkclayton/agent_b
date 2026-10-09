import assert from "node:assert/strict";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { resolve, join } from "node:path";
import { pathToFileURL } from "node:url";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { removeTreeWithinAllowedRoots } from "../tools/removal-guard.mjs";

const within = (inner, outer, tolerance = 1) => inner.left >= outer.left - tolerance && inner.top >= outer.top - tolerance && inner.right <= outer.right + tolerance && inner.bottom <= outer.bottom + tolerance;
const intersects = (a, b, tolerance = 1) => Math.min(a.right, b.right) - Math.max(a.left, b.left) > tolerance && Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > tolerance;
const same = (values, tolerance = 1) => values.length < 2 || Math.max(...values) - Math.min(...values) <= tolerance;

export function auditLayoutSnapshot(snapshot) {
  const failures = [];
  const add = (rule, element, detail) => failures.push({ rule, element, detail });
  const elements = snapshot.elements || [];
  for (const element of elements) {
    if (element.textWhole === false) add("L1", element.id, "text or value is not whole");
    if (element.container && !within(element.box, element.container)) add("L4", element.id, "control is outside its container");
  }
  for (let left = 0; left < elements.length; left++) for (let right = left + 1; right < elements.length; right++) {
    const a = elements[left], b = elements[right];
    if (a.parent === b.id || b.parent === a.id || a.parent && a.parent === b.parent && a.allowSiblingOverlap && b.allowSiblingOverlap) continue;
    if (intersects(a.box, b.box)) add("L2", `${a.id} / ${b.id}`, "visible elements overlap");
  }
  const scrolls = snapshot.scrolls || [];
  for (const region of scrolls) if (region.horizontal) add("L3", region.id, "horizontal scrolling");
  for (let child = 0; child < scrolls.length; child++) for (let parent = 0; parent < scrolls.length; parent++) {
    if (child === parent) continue;
    if (within(scrolls[child].box, scrolls[parent].box) && scrolls[child].box.width < scrolls[parent].box.width - 1 && scrolls[child].box.height < scrolls[parent].box.height - 1) { add("L3", `${scrolls[parent].id} / ${scrolls[child].id}`, "nested scrollers"); child = scrolls.length; break; }
  }
  for (const group of snapshot.groups || []) {
    const rows = group.rows || [];
    const columns = [];
    for (const row of rows) {
      let column = columns.find((candidate) => Math.abs(candidate[0].label.left - row.label.left) <= 2);
      if (!column) columns.push(column = []);
      column.push(row);
    }
    if (columns.some((column) => !same(column.map(({ label }) => label.left)) || !same(column.map(({ control }) => control.left))) || rows.some((row) => row.controlHeights?.length > 1 && !same(row.controlHeights))) add("L5", group.id, "labels and controls do not start together, or controls in one row differ in height");
  }
  for (const gap of snapshot.gaps || []) {
    const floor = gap.compound ? 4 : 8;
    if (gap.value < floor || ![4, 8, 12, 16, 24, 32, 48, 64].includes(Math.round(gap.value))) add("L6", gap.id, `${gap.value}px is off the spacing scale`);
  }
  return failures;
}

export function collectLayoutSnapshot(rootSelector = "body") {
  const root = document.querySelector(rootSelector);
  if (!root) throw new Error(`layout root not found: ${rootSelector}`);
  const visible = (node) => {
    const style = getComputedStyle(node), box = node.getBoundingClientRect();
    return node.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true }) && style.display !== "none" && style.visibility !== "hidden" && box.width > 0 && box.height > 0;
  };
  const rect = (node) => { const box = node.getBoundingClientRect(); return { left: box.left, top: box.top, right: box.right, bottom: box.bottom, width: box.width, height: box.height }; };
  const identity = (node, index = 0) => node.getAttribute("aria-label") || node.getAttribute("data-path") || node.id || node.textContent?.trim().replace(/\s+/g, " ").slice(0, 60) || `${node.tagName.toLowerCase()}-${index}`;
  const candidates = [...root.querySelectorAll("input,select,textarea,button,summary,[data-layout-item]")].filter(visible);
  const canvas = document.createElement("canvas"), context = canvas.getContext("2d");
  const elements = candidates.map((node, index) => { const style = getComputedStyle(node), box = rect(node), value = node.matches("input,textarea") ? node.value : node.matches("select") ? node.selectedOptions[0]?.textContent || "" : node.textContent?.trim().replace(/\s+/g, " ") || ""; context.font = style.font; const textWidth = context.measureText(value).width, nativeInset = node.matches('input[type="number"],select') ? parseFloat(style.fontSize || "0") * 1.5 : 0, available = node.clientWidth - parseFloat(style.paddingLeft || 0) - parseFloat(style.paddingRight || 0) - nativeInset; const textWhole = !value || (node.matches("textarea") ? node.scrollWidth <= node.clientWidth + 1 && node.scrollHeight <= node.clientHeight + 1 : textWidth <= available + 1); const containerNode = node.closest(".setting-row,.connection-row,.connection-editor,.settings-actions,.connection-actions,.settings-content") || root, containerStyle = getComputedStyle(containerNode), container = rect(containerNode); if (/^(auto|scroll|overlay)$/.test(containerStyle.overflowY) && containerNode.scrollHeight > containerNode.clientHeight + 1) { container.top = -1e9; container.bottom = 1e9; } return { id: identity(node, index), box, container, textWhole, parent: candidates.includes(node.parentElement) ? identity(node.parentElement) : "" }; });
  const page = document.scrollingElement;
  const scrollNodes = [page, ...root.querySelectorAll("*")].filter((node, index, all) => {
    if (!node || all.indexOf(node) !== index || !visible(node) || node.matches?.("input,select")) return false;
    if (node === page) return node.scrollHeight > node.clientHeight + 1 || node.scrollWidth > node.clientWidth + 1;
    const style = getComputedStyle(node);
    const scrollsY = /^(auto|scroll|overlay)$/.test(style.overflowY) && node.scrollHeight > node.clientHeight + 1;
    const scrollsX = /^(auto|scroll|overlay)$/.test(style.overflowX) && node.scrollWidth > node.clientWidth + 1;
    return scrollsX || scrollsY;
  });
  const scrolls = scrollNodes.map((node, index) => { const style = getComputedStyle(node); return { id: node === page ? "page" : identity(node, index), box: node === page ? { left: 0, top: 0, right: innerWidth, bottom: innerHeight, width: innerWidth, height: innerHeight } : rect(node), horizontal: node.scrollWidth > node.clientWidth + 1 && (node === page || /^(auto|scroll|overlay)$/.test(style.overflowX)) }; });
  const groups = [...root.querySelectorAll(".connection-fieldset")].map((node, index) => ({ id: identity(node, index), rows: [...node.querySelectorAll(":scope > .setting-row")].filter(visible).map((row) => ({ label: rect(row.querySelector(":scope > label")), control: rect(row.querySelector(":scope > div")), controlHeights: [...row.querySelectorAll(":scope > div input,:scope > div select,:scope > div textarea,:scope > div button")].filter(visible).map((control) => rect(control).height) })) })).filter(({ rows }) => rows.length > 1);
  const gaps = [];
  for (const group of root.querySelectorAll(".settings-actions,.connection-actions")) { const children = [...group.children].filter(visible).sort((a, b) => rect(a).top - rect(b).top || rect(a).left - rect(b).left); for (let index = 1; index < children.length; index++) if (Math.abs(rect(children[index]).top - rect(children[index - 1]).top) <= 1) gaps.push({ id: identity(group), value: Math.round(rect(children[index]).left - rect(children[index - 1]).right), compound: group.classList.contains("connection-actions") }); }
  return { viewport: { width: innerWidth, height: innerHeight }, elements, scrolls, groups, gaps };
}

const args = process.argv.slice(2);
const valueAfter = (flag) => { const at = args.indexOf(flag); return at < 0 ? "" : args[at + 1] || ""; };
const sizes = [{ name: "minimum", width: 304, height: 254 }, { name: "1280x860", width: 1280, height: 860 }, { name: "1920x1080", width: 1920, height: 1080 }];
const safe = (text) => String(text).toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");

async function releaseCheck(path) {
  const report = JSON.parse(await readFile(resolve(path), "utf8"));
  const required = (report.results || []).filter((entry) => entry.blocking !== false);
  const blocking = required.filter((entry) => entry.failures?.length);
  const reviews = report.reviews || [];
  const incomplete = reviews.filter((review) => review.answers?.length !== 5 || review.answers.some((answer) => !String(answer).trim()) || review.ship !== true || review.findings?.some((finding) => !finding.fixed));
  const reviewed = new Set(reviews.map((review) => resolve(review.capture || "")));
  const missing = required.filter((entry) => !reviewed.has(resolve(entry.capture || "")));
  if (blocking.length || incomplete.length || missing.length || !report.results?.length || reviews.length !== required.length) {
    for (const entry of blocking) for (const failure of entry.failures) console.error(`LAYOUT REFUSED: ${entry.page} ${entry.size} ${failure.rule} ${failure.element}`);
    for (const review of incomplete) console.error(`LOOK REFUSED: ${review.capture || "capture"} has an unanswered question, an open finding, or is not marked ship`);
		for (const entry of missing) console.error(`LOOK REFUSED: ${entry.capture || "capture"} has no written review`);
    process.exitCode = 1;
    return;
  }
  console.log(`LAYOUT PASS: ${report.results.length} measured states and ${reviews.length} written looks`);
}

async function attachReviews(reportPath, reviewsPath) {
  const target = resolve(reportPath);
  const report = JSON.parse(await readFile(target, "utf8"));
  const reviews = JSON.parse(await readFile(resolve(reviewsPath), "utf8"));
  assert.ok(Array.isArray(reviews), "look file must be a JSON array");
  report.reviews = reviews;
  await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
  console.log(`LOOK ATTACHED: ${reviews.length} written reviews`);
}

async function openSettings(page, base) {
  await page.goto(`${base}/chat`, { waitUntil: "domcontentloaded" });
  await page.locator(".chat-list-menu-button").click();
  await page.locator(".chat-list-main-menu").getByRole("button", { name: "Settings", exact: true }).click();
  await page.locator("#settings-page").waitFor({ state: "visible" });
}

async function capture(exe, evidence) {
  const { start } = await import("./ui-harness.mjs");
  const root = resolve(evidence), temp = await mkdtemp(join(tmpdir(), "agentb-layout-gate-"));
  await mkdir(root, { recursive: true });
  const harness = await start({ exe: resolve(exe), appRoot: resolve("."), data: join(temp, "data"), modelIDs: ["model-alpha", "model-beta", "model-gamma"], agents: ["Private fixture"] });
  const report = { schema: 1, results: [], reviews: [], other_page_failures: [] };
  const captureState = async (page, pageName, state, size, blocking = pageName === "Connections" || pageName === "Agents") => {
    const file = join(root, `${safe(pageName)}-${safe(state)}-${size.name}.png`);
    await page.screenshot({ path: file, animations: "disabled" });
    const snapshot = await page.evaluate(collectLayoutSnapshot, ".settings-content");
    const failures = auditLayoutSnapshot(snapshot).map((failure) => ({ ...failure }));
    report.results.push({ page: pageName, state, size: size.name, capture: file, blocking, failures });
    if (!blocking) for (const failure of failures) report.other_page_failures.push(`${pageName} ${state} ${size.name} ${failure.rule} ${failure.element}`);
  };
  try {
    for (const size of sizes) {
      const page = await harness.context.newPage();
      await page.setViewportSize(size);
      await openSettings(page, harness.base);
      for (const pageName of ["Agents", "Activity", "Plan", "Users", "Chats", "Notifications", "Security", "About"]) {
        await page.locator(".settings-nav button", { hasText: pageName, exact: true }).click();
        if (pageName === "Agents") {
          await captureState(page, pageName, "agent-b", size, false);
          await page.locator("#panel-agent").selectOption({ label: "Private fixture" });
          await page.locator('.settings-content [data-field="private"]').waitFor();
          await captureState(page, pageName, "added-agent", size, false);
        } else await captureState(page, pageName, "rest", size, false);
      }
      await page.locator('.settings-nav [data-id="connections"]').click();
      await captureState(page, "Connections", "list", size);
      await page.locator('[data-action="connection-toggle"]').first().click();
      await captureState(page, "Connections", "row-open", size);
      await page.locator(".connection-defaults summary").click();
      await captureState(page, "Connections", "defaults-open", size);
      await page.route("**/api/connections/*/probe", (route) => route.fulfill({ status: 400, contentType: "application/json", body: JSON.stringify({ error: "fixture refusal", field: "connections.ui.base_url" }) }));
      await page.locator('[data-action="probe"]').click();
      await page.locator(".connection-test-failure").waitFor();
      await captureState(page, "Connections", "failed-test", size);
      await page.close();
    }
    await writeFile(join(root, "layout-report.json"), `${JSON.stringify(report, null, 2)}\n`);
    console.log(`LAYOUT CAPTURED: ${report.results.length} states at ${root}`);
    if (report.results.some((entry) => entry.page === "Connections" && entry.failures.length)) process.exitCode = 1;
  } finally {
    await harness.stop();
    try { removeTreeWithinAllowedRoots(temp, [tmpdir()], "layout gate cleanup"); } catch {}
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  if (args.includes("--release-check")) await releaseCheck(valueAfter("--release-check"));
  else if (args.includes("--attach-reviews")) await attachReviews(valueAfter("--attach-reviews"), valueAfter("--reviews"));
  else if (args.includes("--capture")) {
    const exe = valueAfter("--capture"), evidence = valueAfter("--evidence");
    assert.ok(exe && evidence, "usage: node tests/layout-gate.mjs --capture EXE --evidence DIR");
    await capture(exe, evidence);
  } else {
    console.error("usage: node tests/layout-gate.mjs --capture EXE --evidence DIR | --attach-reviews REPORT --reviews LOOK.json | --release-check REPORT");
    process.exitCode = 2;
  }
}
