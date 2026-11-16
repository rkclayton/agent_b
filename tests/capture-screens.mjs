import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { resolve, join } from 'node:path';
import { start } from './ui-harness.mjs';

const [exe, directory] = process.argv.slice(2);
if (!exe || !directory) throw new Error('usage: node tests/capture-screens.mjs EXE DIRECTORY');
const root = resolve(directory), data = join(root, 'fixture'), index = [], unreachable = [];
await mkdir(join(data, 'profiles', 'fixture'), { recursive: true });
const config = JSON.parse(await readFile('harness.example.json', 'utf8'));
Object.assign(config, { profiles: { active: 'fixture', names: ['fixture'] }, telemetry: { enabled: false }, updates: { auto_check: false }, workspace: 'scratch', log_dir: 'logs', connections: [{ id: 'ui', label: 'Local PC', model: 'model-alpha', base_url: 'http://127.0.0.1:1', context: { n_ctx: 32768 }, probe_mode: 'off' }], agents: [{ name: 'UI', b: 'ui' }, { name: 'Custom', b: 'ui' }] });
config.shell.service_account.enabled = false;
config.deliver.exchange_folder = join(data, 'exchange');
await writeFile(join(data, 'harness.json'), JSON.stringify(config));
await writeFile(join(data, 'profiles', 'fixture', 'profile.json'), JSON.stringify({ name: 'fixture', agents: config.agents, deliver: config.deliver, notifications: config.notifications }));
const harness = await start({ exe: resolve(exe), appRoot: resolve('.'), data, reuseConfig: true, modelIDs: ['model-alpha'], connectionModel: 'model-alpha' });
const slug = value => value.toLowerCase().replace(/[^a-z0-9]+/g, '-');
async function capture(page, name, route) {
  await page.waitForTimeout(150);
  const scrollers = await page.locator('body *').evaluateAll(nodes => nodes.filter(n => n.checkVisibility() && n.clientHeight > 0 && !n.matches('input,select,textarea,option') && /auto|scroll/.test(getComputedStyle(n).overflowY) && n.scrollHeight > n.clientHeight + 2).map(n => { n.scrollTop = 0; return { id: n.id, cls: n.className, max: n.scrollHeight - n.clientHeight, step: Math.max(100, n.clientHeight - 40) }; }));
  let part = 0;
  const save = async () => {
    const file = `${slug(name)}-${page.viewportSize().width}-${++part}.png`;
    // No machine profile paths or account names may enter an evidence image.
    const text = await page.locator('body').innerText();
    if (/C:\\Users\\/i.test(text)) throw new Error('profile path visible; state needs a stand-in');
    await page.screenshot({ path: join(root, file), animations: 'disabled' });
    index.push(`| [${file}](${file}) | ${name}, part ${part} | ${route} |`);
  };
  await save();
  for (const scroll of scrollers) {
    let previous = 0;
    for (let y = scroll.step; y < scroll.max + scroll.step; y += scroll.step) {
      const top = await page.evaluate(({ scroll, y }) => { const node = scroll.id ? document.getElementById(scroll.id) : [...document.querySelectorAll('body *')].find(n => n.className === scroll.cls); if (!node) return 0; node.scrollTop = Math.min(y, scroll.max); return node.scrollTop; }, { scroll, y });
      if (top === previous) break;
      previous = top; await save();
    }
  }
}
async function attempt(name, work) { try { await work(); } catch (error) { unreachable.push(`${name}: ${String(error.message).split('\n')[0]}`); } }
async function section(page, id) { await page.evaluate(id => document.dispatchEvent(new CustomEvent('settings.open', { detail: { section: id } })), id); await page.locator('#settings-page').waitFor({ state: 'visible' }); }
try {
  for (const width of [1280, 304]) {
    const page = await harness.context.newPage(); await page.setViewportSize({ width, height: width === 304 ? 254 : 860 }); page.setDefaultTimeout(4000);
    await page.goto(`${harness.base}/chat`); await page.waitForTimeout(500);
    await page.evaluate(async () => { const { store, reduce } = await import(performance.getEntriesByType('resource').map(entry => entry.name).find(name => name.endsWith('/js/bus.js'))); globalThis.captureState = { ...store }; reduce({ type: 'snapshot', data: { ...store, incremental: false, sessions: {} } }); });
    await attempt('main empty', () => capture(page, 'main-empty', 'Open /chat without selecting a chat'));
    await page.evaluate(async () => { const { reduce } = await import(performance.getEntriesByType('resource').map(entry => entry.name).find(name => name.endsWith('/js/bus.js'))); reduce({ type: 'snapshot', data: { ...globalThis.captureState, incremental: false } }); });
    await page.locator('.chat-list-row[data-session]').first().click();
    await capture(page, 'main-idle', 'Select the stand-in chat');
    for (const id of (process.argv.includes('--chat-only') ? [] : ['activity', 'plan', 'connections', 'profiles', 'skills', 'chats', 'notifications', 'shell', 'about'])) await attempt(`${id} ${width}`, async () => {
      await section(page, id); await capture(page, `${id}-closed`, `Settings > ${id}`);
      if (id === 'profiles') { await page.locator('#panel-agent').selectOption({ label: 'Custom' }); await capture(page, 'profiles-custom', 'Profiles > Custom'); }
      if (id === 'connections') {
        for (const action of ['server-toggle', 'connection-toggle']) { const control = page.locator(`[data-action="${action}"]`).first(); if (await control.count()) { await control.click(); await capture(page, `connections-${action}`, `Connections > ${action}`); } }
      }
      const folds = await page.locator('.settings-content details').count();
      for (let n = 0; n < folds; n++) {
        const fold = page.locator('.settings-content details').nth(n); const label = await fold.locator(':scope > summary').innerText();
        await fold.evaluate(node => { node.open = true; }); await capture(page, `${id}-${n}-${label}-open`, `${id} > ${label}`);
      }
      await capture(page, `${id}-all-open`, `Settings > ${id}, every fold open`);
      for (const menu of await page.locator('.settings-content [data-action="setting-menu-toggle"]').all()) { const label = await menu.getAttribute('data-menu-path'); await menu.evaluate(n => n.click()); await capture(page, `${id}-${label}-menu`, `${id} > ${label}`); await page.keyboard.press('Escape'); }
      if (id === 'activity') for (const control of ['clear-stats', 'flush-memory']) { await page.locator(`#${control}`).evaluate(n => n.click()); await page.locator('.confirm-popover').waitFor(); await capture(page, `activity-${control}`, `Activity > ${control}`); await page.keyboard.press('Escape'); }
      if (id === 'profiles') for (const action of ['add', 'duplicate', 'delete']) { await page.locator(`#panel-roles [data-action="${action}"]`).evaluate(n => n.click()); await page.locator('.confirm-popover').waitFor(); await capture(page, `profile-${action}`, `Profiles > ${action}`); await page.keyboard.press('Escape'); }
      if (id === 'plan' || id === 'skills') {
        await page.route('**/api/folder-picker', route => route.fulfill({ json: { path: 'C:\\work\\acme' } }));
        if (id === 'plan') { await page.route('**/api/plans', route => route.request().method() === 'POST' ? route.fulfill({ json: { folder: 'C:\\work\\acme', repository: { root: 'C:\\work\\acme', branch: 'main' }, plan: 'plan.md', agents: 'AGENTS.md' } }) : route.continue()); await page.locator('#plan-add').click(); await page.locator('#plan-add-preview').waitFor(); }
        else { await page.route('**/api/skills', route => route.request().method() === 'POST' ? route.fulfill({ json: { hermes: { rows: [{ id: 'fixture', name: 'Fixture skill', description: 'Stand-in skill.', valid: true, files: 1 }] } } }) : route.continue()); await page.locator('[data-action="skill-add-pick"]').click(); await page.locator('.skill-add-preview').waitFor(); }
        await capture(page, `${id}-add-preview`, `${id} > Add (stand-in picker and preview)`);
      }
      if (id === 'shell') { await page.locator('#credential-provider').selectOption('google', { force: true }); await capture(page, 'security-google', 'Security > Add signed-in credential > Google'); }
      if (id === 'profiles') { await page.locator('[data-action="profile-memory-toggle"]').click(); await capture(page, 'profiles-memory', 'Profiles > memory > Open'); }
      if (id === 'connections') {
        await page.route('**/api/connections/*/probe', route => route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: 'Stand-in server refused the test.' }) }));
        await page.locator('[data-action="probe"]').first().click(); await page.locator('[data-action="error-details"]').first().click(); await capture(page, 'connections-error-details', 'Connection > Test (stand-in refusal) > details'); await page.locator('[data-action="close-error-details"]').click();
      }
      if (id === 'chats') { await page.locator('[data-action="delete-all-chats"]').evaluate(n => n.click()); await capture(page, 'chats-delete-confirm', 'Settings > Chats > Delete all chats'); await page.keyboard.press('Escape'); }
      if (id === 'about') for (const [name, update] of Object.entries({ available: { available: true, version: 'v9.9.9' }, downloading: { available: true, version: 'v9.9.9', installing: true, step: 'download', line: 'Downloading', downloaded_bytes: 1024, total_bytes: 4096 }, failed: { available: true, version: 'v9.9.9', error: 'Fixture download failed' } })) {
        await page.evaluate(async update => { const { reduce } = await import(performance.getEntriesByType('resource').map(entry => entry.name).find(name => name.endsWith('/js/bus.js'))); reduce({ type: 'update.changed', data: { ...update, enabled: true } }); }, update);
        await capture(page, `about-${name}`, `Stand-in update.changed: ${name}`);
      }
    });
    if (await page.locator('#settings-page').isVisible()) await page.locator('#settings-page [data-action="close"]').click();
    await attempt(`header ${width}`, async () => { await page.locator('.chat-list-menu-button').click(); await capture(page, 'header-menu', 'Open main menu'); await page.keyboard.press('Escape'); });
    await attempt(`row menu ${width}`, async () => { await page.locator('.chat-list-more').first().evaluate(node => node.click()); await capture(page, 'chat-row-menu', 'Stand-in chat > menu'); await page.keyboard.press('Escape'); });
    for (const action of ['Rename', 'Move to folder', 'Delete']) await attempt(`chat ${action} ${width}`, async () => {
      await page.locator('.chat-list-more').first().evaluate(node => node.click()); await page.locator('.chat-list-row-menu:visible').getByRole('button', { name: action, exact: true }).evaluate(node => node.click()); await capture(page, `chat-${action}`, `Chat menu > ${action}`); await page.keyboard.press('Escape');
    });
    await attempt(`Chats ${width}`, async () => { await page.getByText('View all', { exact: true }).click(); await capture(page, 'chats-window', 'Chat list > View all'); await page.locator('.chats-window-tabs button').last().click(); await capture(page, 'chats-archived', 'Chats > Archived'); await page.getByRole('button', { name: 'Close Chats', exact: true }).click(); });
    for (const mode of ['tool-collapsed', 'tool-expanded', 'approval', 'failed', 'unreachable']) await attempt(`${mode} ${width}`, async () => {
      await page.evaluate(async mode => {
        const { store, reduce } = await import(performance.getEntriesByType('resource').map(entry => entry.name).find(name => name.endsWith('/js/bus.js'))); const id = store.active, old = store.sessions[id], runID = 'fixture-run';
        const tool = { type: 'tool', key: 'fixture-tool', run_id: runID, name: 'read_file', args: { path: 'C:\\work\\acme\\readme.txt' }, result: { ok: true, ms: 20 }, content: 'Stand-in file contents' };
        const approval = { type: 'approval', key: 'fixture-approval', run_id: runID, event: { type: 'approval.required', data: { call_id: 'fixture-call', name: 'shell.operator_override', boundary_escape: true, args: { command: 'Get-Content C:\\work\\acme\\readme.txt', outside_folder_card: true, outside_folders: ['C:\\work\\acme'] } } } };
        const notice = { type: 'notice', key: 'fixture-notice', run_id: runID, event: { type: 'run.stopped', data: { reason: mode === 'unreachable' ? 'model_unreachable' : 'error', error: mode === 'unreachable' ? 'Stand-in model is unavailable' : 'Stand-in run failed' } } };
        const chat = mode.startsWith('tool') ? [tool] : mode === 'approval' ? [approval] : [notice];
        chat.unshift({ type: 'user', key: `fixture-request-${mode}`, text: 'Inspect the stand-in file.', run_id: runID });
        reduce({ type: 'snapshot', data: { ...store, incremental: false, sessions: { ...store.sessions, [id]: { ...old, activity: { stage: 'execute', stage_state: 'enter', active_tool: 'read_file', completed_stages: [] }, history_start: 0, history_end: chat.length, history_total: chat.length, cursor: { generation: 'capture', offset: Date.now() }, chat, pending_approval: mode === 'approval' ? approval : null, run: { ...old.run, run_id: runID, status: mode.startsWith('tool') ? 'running' : mode === 'approval' ? 'paused' : 'idle', last_stop_reason: mode === 'unreachable' ? 'model_unreachable' : 'error' } } } } });
      }, mode); await page.waitForTimeout(250);
      if (mode === 'tool-expanded') for (let depth = 0; depth < 3; depth++) { for (const fold of await page.locator('#chat-log details').all()) await fold.evaluate(n => { n.open = true; }); for (const button of await page.locator('#chat-log [aria-expanded="false"]').all()) await button.evaluate(node => node.click()); }
      await capture(page, `main-${mode}`, `Stand-in projected session: ${mode}`);
    });
    await attempt(`Setup ${width}`, async () => {
      await page.unroute('**/api/connections/*/probe');
      await page.goto(`${harness.base}/setup?from=settings`); await page.getByRole('button', { name: 'Connect', exact: true }).waitFor(); await capture(page, 'setup-choice', 'Connections > Open setup guide');
      await page.getByRole('button', { name: /More options/ }).click(); await capture(page, 'setup-more', 'Setup > More options');
      await page.getByLabel('Address').fill(`http://127.0.0.1:${harness.modelPort}`); await page.getByLabel('Model', { exact: true }).fill('model-alpha'); await page.getByRole('button', { name: 'Connect', exact: true }).click();
      await page.getByRole('button', { name: 'Next', exact: true }).waitFor({ timeout: 15000 }); await capture(page, 'setup-checks', 'Setup > Connect to stand-in'); await page.getByRole('button', { name: 'Next', exact: true }).click(); await capture(page, 'setup-ready', 'Setup > checks > Next');
    });
    await page.close();
  }
} finally {
  unreachable.push('Native folder/file pickers, credential browser sign-in and elevation dialogs: external OS surfaces; not invoked by this headless capture.', 'Real model install/download and real phone pairing: excluded by this order; require stand-in routes before capture.');
  await writeFile(join(root, 'index.md'), `# Current-build screens\n\n| File | Shows | Reached by |\n| --- | --- | --- |\n${index.join('\n')}\n\n## Unreachable\n\n${unreachable.map(line => `- ${line}`).join('\n')}\n`);
  await harness.stop(); console.log(`${index.length} captures; ${unreachable.length} unreachable entries`);
}
