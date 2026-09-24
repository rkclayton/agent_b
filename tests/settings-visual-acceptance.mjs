import assert from "node:assert/strict";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { start } from "./ui-harness.mjs";

const [exe, evidenceRoot, production = "http://127.0.0.1:8790"] = process.argv.slice(2);
assert.ok(exe && evidenceRoot, "usage: node tests/settings-visual-acceptance.mjs EXE EVIDENCE [PRODUCTION]");

const expected = ["Agents", "Activity", "Connections", "Profiles", "Chats", "Notifications", "Security", "About"];
const safeName = (value) => value.toLowerCase().replace(/[^a-z0-9]+/g, "-");
const temp = await mkdtemp(join(tmpdir(), "agentb-settings-visual-"));
const report = { production, candidate: "", captures: [], geometry: [] };

async function openSettings(page, base) {
  await page.goto(`${base}/chat`, { waitUntil: "domcontentloaded" });
  await page.locator(".shell-settings").waitFor({ state: "visible" });
  await page.locator(".shell-settings").click();
  await page.locator("#settings-page").waitFor({ state: "visible" });
}

async function captureSet(context, base, phase, sections) {
  for (const viewport of [{ name: "wide", width: 1280, height: 900 }, { name: "narrow", width: 320, height: 900 }]) {
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    console.log(`capture ${phase}/${viewport.name}`);
    await page.setViewportSize(viewport);
    await openSettings(page, base);
    const found = await page.locator(".settings-nav button").allInnerTexts();
    assert.deepEqual(found, sections, `${phase} settings navigation`);
    const directory = resolve(evidenceRoot, phase, viewport.name);
    await mkdir(directory, { recursive: true });
    for (const section of sections) {
      await page.locator(".settings-nav button", { hasText: section, exact: true }).click();
      await page.locator(".settings-content").waitFor({ state: "visible" });
      const path = join(directory, `${safeName(section)}.png`);
      await page.screenshot({ path, animations: "disabled" });
      report.captures.push({ phase, viewport: viewport.name, section, path });
      const geometry = await page.evaluate(() => {
        const rows = [...document.querySelectorAll(".settings-content .setting-row")].map((row) => {
          const label = row.querySelector(":scope > label");
          const control = row.querySelector(":scope > div");
          const rr = row.getBoundingClientRect();
          const lr = label?.getBoundingClientRect();
          const cr = control?.getBoundingClientRect();
          return { row: rr.toJSON(), label: lr?.toJSON(), control: cr?.toJSON(), title: row.title };
        });
        return {
          documentOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
          navOverflow: document.querySelector(".settings-nav").scrollWidth - document.querySelector(".settings-nav").clientWidth,
          rows,
          subheads: [...document.querySelectorAll(".settings-subhead")].map((node) => ({ text: node.innerText.trim(), title: node.title })),
        };
      });
      assert.ok(geometry.documentOverflow <= 0, `${phase}/${viewport.name}/${section}: document overflows`);
      for (const row of geometry.rows) {
        if (phase !== "after") continue;
        assert.ok(row.label && row.control, `${phase}/${viewport.name}/${section}: row grammar`);
        assert.ok(row.title.trim(), `${phase}/${viewport.name}/${section}: row hover text`);
        assert.ok(row.control.x >= row.label.x, `${phase}/${viewport.name}/${section}: control precedes label`);
      }
      report.geometry.push({ phase, viewport: viewport.name, section, ...geometry });
    }
    await page.close();
  }
}

let candidate;
try {
  candidate = await start({ exe: resolve(exe), appRoot: resolve("."), data: join(temp, "data") }).catch((error) => {
    console.error(error);
    throw error;
  });
  report.candidate = candidate.base;
  await captureSet(candidate.context, candidate.base, "after", expected);
  const productionContext = await candidate.browser.newContext();
  await captureSet(productionContext, production, "before", ["Agents", "Activity", "Connections", "Profiles", "Context", "Run & approval", "Delivery", "Notifications", "Security", "About"]);
  await productionContext.close();
  await mkdir(resolve(evidenceRoot), { recursive: true });
  await writeFile(resolve(evidenceRoot, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
  console.log(`PASS settings visual acceptance: ${report.captures.length} screenshots, ${report.geometry.length} geometry checks`);
} finally {
  if (candidate) {
    candidate.app.kill();
    await Promise.race([candidate.stop().catch(() => {}), new Promise((done) => setTimeout(done, 5000))]);
  }
  await rm(temp, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }).catch(() => {});
}
