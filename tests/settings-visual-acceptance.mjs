import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtemp, mkdir, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { start, waitFor } from "./ui-harness.mjs";
import { removeTreeWithinAllowedRoots } from "../tools/removal-guard.mjs";

const [exe, evidenceRoot, production = "http://127.0.0.1:8790"] = process.argv.slice(2);
assert.ok(exe && evidenceRoot, "usage: node tests/settings-visual-acceptance.mjs EXE EVIDENCE [PRODUCTION]");
const sections = ["Activity", "Plan", "Connections", "Profiles", "Skills", "Chats", "Notifications", "Security", "About"];
const viewports = [{ width: 1280, height: 860 }, { width: 1920, height: 1080 }, { width: 2560, height: 1400 }];
const safeName = (value) => value.toLowerCase().replace(/[^a-z0-9]+/g, "-");
const temp = await mkdtemp(join(tmpdir(), "agentb-settings-visual-"));
const report = { production, candidate: "", captures: [], geometry: [], sentences: { before: {}, after: {} }, keptLines: [] };

async function openSettings(page, base) {
  await page.goto(`${base}/chat`, { waitUntil: "domcontentloaded" });
  await page.locator(".chat-list-menu-button").click();
  await page.locator(".chat-list-main-menu").getByRole("button", { name: "Settings", exact: true }).click();
  await page.locator("#settings-page").waitFor({ state: "visible" });
}

async function skillSource(root, name, description, extra = false) {
  const directory = join(root, name);
  await mkdir(directory, { recursive: true });
  await writeFile(join(directory, "SKILL.md"), `---\nname: ${name}\ndescription: ${description}\n---\nProcedure.\n`);
  if (extra) await writeFile(join(directory, "notes.txt"), "fixture\n");
  return directory;
}

async function activityFixture(dataRoot) {
	const directory = join(dataRoot, "stats");
	await mkdir(directory, { recursive: true });
	const memory = join(dataRoot, "memory");
	await mkdir(memory, { recursive: true });
	await writeFile(join(memory, "user.md"), "- 2026-10-15 The repository CI gate says do not bypass tools/check-release.mjs.  [scope: repository]\n");
	await writeFile(join(directory, "ui.json"), `${JSON.stringify({
		version: 1,
		agent_id: "ui",
		agent: {
			chats: 2, runs: 3, turns: 12,
			tools: {
				read_file: { calls: 9, failures: 1, last_used: "2026-10-12T12:00:00Z" },
				list_dir: { calls: 3, failures: 0, last_used: "2026-10-12T11:00:00Z" },
				edit_file: { calls: 3, failures: 1, last_used: "2026-10-12T10:00:00Z" },
				shell: { calls: 0, failures: 0 },
			},
			approvals_raised: 2, approvals_approved: 1, operator_grants: 1,
			compactions: 1, prompt_tokens: 1200, completion_tokens: 300, cached_tokens: 400,
			wall_ms: 9000, model_response_ms: [100, 200], worker_reliability: {},
		},
		connections: {},
	}, null, 2)}\n`);
}

async function fixtures(candidate) {
  const source = join(temp, "skill-sources");
  const alpha = await skillSource(source, "alpha", "Produces a concise stand-in report.", true);
  await skillSource(source, "bravo", "Checks a stand-in result before it is used.");
  await skillSource(source, "long-description", "A".repeat(300));
  const replacement = join(temp, "replacement");
  await skillSource(replacement, "alpha", "Replacement description for the stored stand-in skill.", true);
  const headers = { "X-AgentB-Mutation-Token": candidate.mutationToken };
  for (const path of [alpha, join(source, "bravo"), join(source, "long-description")]) {
    const response = await candidate.context.request.post(`${candidate.base}/api/skills`, { headers, data: { action: "import", agent_id: "ui", path } });
    assert.equal(response.status(), 200, await response.text());
  }
  const state = await candidate.getState();
  const profileName = state.config?.profiles?.active;
  assert.ok(profileName, "active user profile is named");
  const invalid = join(candidate.dataRoot, "profiles", profileName, "skills", "invalid-skill");
  await mkdir(invalid, { recursive: true });
  await writeFile(join(invalid, "SKILL.md"), "---\ndescription: Missing its name.\n---\n");

  const repository = join(temp, "plan-repository");
  await mkdir(repository, { recursive: true });
  await writeFile(join(repository, "PLAN.md"), "# Plan\n\n- [ ] Stand-in task\n");
  await writeFile(join(repository, "AGENTS.md"), "# Instructions\n");
  execFileSync("git", ["init", "-b", "main"], { cwd: repository, windowsHide: true, stdio: "ignore" });
  execFileSync("git", ["config", "user.name", "Fixture"], { cwd: repository, windowsHide: true, stdio: "ignore" });
  execFileSync("git", ["config", "user.email", "someone@example.org"], { cwd: repository, windowsHide: true, stdio: "ignore" });
  execFileSync("git", ["add", "."], { cwd: repository, windowsHide: true, stdio: "ignore" });
  execFileSync("git", ["commit", "-m", "Fixture"], { cwd: repository, windowsHide: true, stdio: "ignore" });
  return { replacement: join(replacement, "alpha"), repository };
}

async function sectionFacts(page) {
  return page.evaluate(() => {
    const panel = document.querySelector(".settings-page").getBoundingClientRect();
    const rows = [...document.querySelectorAll(".settings-content .setting-row")].filter((row) => {
      const rect = row.getBoundingClientRect();
      return rect.width > 0 && rect.height > 0;
    }).map((row) => {
      const label = row.querySelector(":scope > label")?.getBoundingClientRect();
      const control = row.querySelector(":scope > div")?.getBoundingClientRect();
      return { gap: label && control ? control.x - label.right : null, title: row.title };
    });
    const text = document.querySelector(".settings-content").innerText;
    const sentences = (text.match(/[^\n.!?]+[.!?](?:\s|$)/g) || []).map((value) => value.trim());
		const activity = [...document.querySelectorAll("#panel-tool-counters tr")].map((row) => {
			const cells = row.querySelectorAll("td");
			const bar = row.querySelector(".activity-call-bar");
			const style = cells[1] ? getComputedStyle(cells[1]) : null;
			return { name: cells[0]?.innerText, calls: Number(cells[1]?.innerText || 0), bar: bar?.getBoundingClientRect().width || 0,
				seat: cells[1] ? cells[1].getBoundingClientRect().width - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight) : 0 };
		});
    return { panel: panel.toJSON(), rows, sentences, activity, overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth };
  });
}

async function captureCandidate(candidate, paths) {
  let planChatsMade = false;
  for (const viewport of viewports) {
    const page = await candidate.context.newPage();
    page.setDefaultTimeout(10000);
    await page.setViewportSize(viewport);
    await openSettings(page, candidate.base);
    assert.deepEqual(await page.locator(".settings-nav button").allInnerTexts(), sections);
    const folder = resolve(evidenceRoot, `${viewport.width}x${viewport.height}`);
    await mkdir(folder, { recursive: true });
    for (const section of sections) {
      await page.locator(".settings-nav button", { hasText: section, exact: true }).click();
		if (section === "Activity") await page.locator("#panel-tool-counters tr").first().waitFor();
      const facts = await sectionFacts(page);
      assert.ok(Math.abs(facts.panel.width - viewport.width * .8) <= 2, `${section} panel width at ${viewport.width}`);
      assert.ok(Math.abs(facts.panel.height - viewport.height * .8) <= 2, `${section} panel height at ${viewport.height}`);
      assert.ok(facts.overflow <= 0, `${section} document overflow at ${viewport.width}`);
      if (viewport.width === 1920) {
        const gaps = facts.rows.map((row) => row.gap).filter(Number.isFinite);
        assert.ok(gaps.every((gap) => gap <= 16), `${section} row gap ${Math.max(...gaps, 0)}px`);
        if (gaps.length) assert.ok(Math.max(...gaps) - Math.min(...gaps) <= 1, `${section} row gaps differ: ${gaps}`);
        report.sentences.after[section] = facts.sentences.length;
        for (const sentence of facts.sentences) report.keptLines.push({ section, sentence });
			if (section === "Activity") {
				assert.deepEqual(facts.activity.slice(0, 3).map((row) => [row.name, row.calls]), [["read_file", 9], ["edit_file", 3], ["list_dir", 3]]);
				const total = facts.activity.reduce((sum, row) => sum + row.calls, 0);
				for (const row of facts.activity.filter((item) => item.calls)) assert.ok(Math.abs(row.bar - row.seat * row.calls / total) <= 1, `${row.name} bar ${row.bar}px for ${row.calls}/${total}`);
			}
      }
      const path = join(folder, `${safeName(section)}.png`);
      await page.screenshot({ path, animations: "disabled" });
      report.captures.push(path);
      report.geometry.push({ viewport, section, ...facts });
    }

    if (viewport.width !== 2560) {
      await page.locator(".settings-nav button", { hasText: "Skills", exact: true }).click();
      await page.locator(".skill-table-row .skill-badge").first().click();
      let path = join(folder, "skills-open-invalid-shipped.png");
      await page.screenshot({ path, animations: "disabled" }); report.captures.push(path);
      await page.route("**/api/folder-picker", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ path: paths.replacement }) }));
      await page.getByRole("button", { name: "Add skill", exact: true }).click();
		const replace = page.getByRole("button", { name: "Replace", exact: true });
		await replace.waitFor();
		await replace.scrollIntoViewIfNeeded();
      path = join(folder, "skills-replace.png");
      await page.screenshot({ path, animations: "disabled" }); report.captures.push(path);
      await page.unroute("**/api/folder-picker");
		await page.locator('[data-action="skill-remove"][data-id="alpha"]').click();
      path = join(folder, "skills-remove-question.png");
      await page.screenshot({ path, animations: "disabled" }); report.captures.push(path);

      await page.locator(".settings-nav button", { hasText: "Profiles", exact: true }).click();
      path = join(folder, "profiles-badges.png");
      await page.screenshot({ path, animations: "disabled" }); report.captures.push(path);
		await page.locator("#panel-tools-fold > summary").click();
		await page.locator(".panel-tool-line").first().click();
		path = join(folder, "profiles-tool-open.png");
		await page.screenshot({ path, animations: "disabled" }); report.captures.push(path);
		await page.locator("#panel-tools-fold > summary").click();
      await page.locator('[data-action="profile-memory-toggle"]').click();
		await page.locator(".profile-memory-block").scrollIntoViewIfNeeded();
      path = join(folder, "profiles-memory-open.png");
      await page.screenshot({ path, animations: "disabled" }); report.captures.push(path);

      await page.locator(".settings-nav button", { hasText: "Plan", exact: true }).click();
      await page.route("**/api/folder-picker", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ path: paths.repository }) }));
		await page.locator("#plan-add").evaluate((button) => button.click());
		await page.locator("#plan-add-preview").waitFor({ state: "visible" });
      path = join(folder, "plan-add-four-steps.png");
      await page.screenshot({ path, animations: "disabled" }); report.captures.push(path);
		await page.locator("#plan-add-confirm").evaluate((button) => button.click());
		const plan = await waitFor(async () => {
			const response = await candidate.context.request.get(`${candidate.base}/api/plans`);
			const values = await response.json();
			return values.find((item) => item.repo?.toLowerCase() === paths.repository.toLowerCase());
		}, "the visual plan was registered");
		if (!planChatsMade) {
			for (const label of ["First repository chat", "Second repository chat"]) {
				const created = await candidate.context.request.post(`${candidate.base}/api/sessions`, {
					headers: { "X-AgentB-Mutation-Token": candidate.mutationToken },
					data: { agent_id: "ui", label, role: "c", plan_id: plan.id },
				});
				assert.equal(created.status(), 201, await created.text());
				const session = (await created.json()).session;
				const sent = await candidate.context.request.post(`${candidate.base}/api/message`, {
					headers: { "X-AgentB-Mutation-Token": candidate.mutationToken },
					data: { session_id: session.id, text: `Work in ${label}.` },
				});
				assert.equal(sent.status(), 202, await sent.text());
				await waitFor(async () => (await candidate.getState()).sessions?.[session.id]?.run?.status === "idle", `${label} completed`);
			}
			planChatsMade = true;
		}
		await page.reload({ waitUntil: "domcontentloaded" });
		await openSettings(page, candidate.base);
		await page.locator(".settings-nav button", { hasText: "Plan", exact: true }).click();
		await page.locator("#plan-chats tr").first().waitFor();
		await page.locator("#plan-changes tr").first().waitFor();
		path = join(folder, "plan-with-chats-changes.png");
		await page.screenshot({ path, animations: "disabled" }); report.captures.push(path);
    }
    await page.close();
  }

  const narrow = await candidate.context.newPage();
  await narrow.setViewportSize({ width: 600, height: 860 });
  await openSettings(narrow, candidate.base);
  const panel = await narrow.locator(".settings-page").boundingBox();
  assert.ok(Math.abs(panel.x) <= 2 && Math.abs(panel.width - 600) <= 2 && Math.abs(panel.y + panel.height - 860) <= 2, `600px panel is x=${panel.x}, y=${panel.y}, ${panel.width}x${panel.height}`);
  await narrow.close();
}

async function countProduction(candidate) {
  const context = await candidate.browser.newContext({ viewport: { width: 1920, height: 1080 } });
  const page = await context.newPage();
  await openSettings(page, production === "candidate" ? candidate.base : production);
  for (const section of await page.locator(".settings-nav button").allInnerTexts()) {
    await page.locator(".settings-nav button", { hasText: section, exact: true }).click();
    report.sentences.before[section] = (await sectionFacts(page)).sentences.length;
  }
  await context.close();
}

let candidate;
try {
	const dataRoot = join(temp, "data");
	await activityFixture(dataRoot);
  candidate = await start({ exe: resolve(exe), appRoot: resolve("."), data: dataRoot, agents: ["Review"], memoryEnabled: true });
  report.candidate = candidate.base;
  const paths = await fixtures(candidate);
  await captureCandidate(candidate, paths);
  await countProduction(candidate);
  await mkdir(resolve(evidenceRoot), { recursive: true });
  await writeFile(resolve(evidenceRoot, "report.json"), `${JSON.stringify(report, null, 2)}\n`);
  console.log(`PASS settings visual acceptance: ${report.captures.length} screenshots, ${report.geometry.length} page measurements`);
} finally {
  if (candidate) {
    candidate.app.kill();
    await Promise.race([candidate.stop().catch(() => {}), new Promise((done) => setTimeout(done, 5000))]);
  }
  try { removeTreeWithinAllowedRoots(temp, [tmpdir()], "settings visual fixture cleanup"); } catch { /* closing handles are non-fatal */ }
}
