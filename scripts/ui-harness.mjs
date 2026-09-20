// A disposable harness for UI scenarios (v1.1.2). One server, one fake model
// server, one browser context — started from a data root the caller owns and
// removed by the caller. It exists so the scenarios in this order (2gf's eight
// routes, 2gi's three Plan renders, 2gh's menu measurements) share one start-up
// instead of each growing its own copy of it.
//
// The model server is a switch, not a fixture: `reachable: false` binds nothing
// and the profile points at a dead port, which is the operator's condition in
// 2gf ("model is unavailable which may contribute").
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdir, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "@playwright/test";

export const sleep = (ms) => new Promise((done) => setTimeout(done, ms));

export async function freePort() {
  const probe = createServer();
  await new Promise((done) => probe.listen(0, "127.0.0.1", done));
  const port = probe.address().port;
  await new Promise((done) => probe.close(done));
  // Production is 8790 and is never what a scenario talks to.
  assert.ok(port !== 8790, "the disposable harness must not take production's port");
  return port;
}

export async function waitFor(check, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  let last = null;
  while (Date.now() < deadline) {
    try { const value = await check(); if (value) return value; } catch (error) { last = error; }
    await sleep(25);
  }
  throw new Error(`${label} timeout${last ? `: ${last.message}` : ""}`);
}

async function stopChild(child) {
  // Hard stop (12): a process this harness started is ended by its PID and by
  // nothing else. child.kill() signals exactly that PID.
  if (!child || child.exitCode !== null) return;
  child.kill();
  await Promise.race([new Promise((done) => child.once("exit", done)), sleep(3000)]);
}

const TOOLSET = ["read_file", "list_dir", "write_file", "edit_file", "search_text", "shell", "remember", "recall", "fetch_url", "find_files", "run_script", "call_service"];

function configFor({ appPort, modelPort, data, workspace, agents = [] }) {
  return {
    config_version: 6, listen: `127.0.0.1:${appPort}`, workspace, log_dir: join(data, "logs"),
    servers: [{
      id: "ui", label: "UI", base_url: `http://127.0.0.1:${modelPort}`, model: "ui-harness", credential: "", request_timeout_s: 2, probe_mode: "off",
      sampling: { thinking: { temperature: .6, top_p: .95, top_k: 20, min_p: 0, presence_penalty: 0, repeat_penalty: 1 }, nonthinking: { temperature: .7, top_p: .8, top_k: 20, min_p: 0, presence_penalty: 0, repeat_penalty: 1 } },
      reasoning: { control: "auto", enabled: false, effort: "medium", valid_efforts: [], preserve: false },
      context: { n_ctx: 32768, reserve_output: 10240 }, system_prompt_override: "",
      capabilities: { server: "ui-harness", props: true, n_ctx: 32768, tokenize: true, apply_template: true, apply_template_tools: true, streaming: true, tool_calls: true, grammar_constrained: false, cached_tokens: true, timings: false, prompt_progress: false, document_input: false, image_input: false, reasoning_control: "", valid_efforts: [], overflow_behavior: "error", probed_at: new Date().toISOString(), findings: [] },
    }],
    services: {}, agents: [{ name: "UI", b: "ui", toolset: TOOLSET }, ...[...new Set(agents)].filter((name) => name.toLowerCase() !== "ui").map((name) => ({ name, b: "ui", toolset: TOOLSET }))], chat: { auto_rename: false },
    run: { max_turns: 4, cycle_window: 8, max_consecutive_tool_errors: 3, max_concurrent: 1, queue_depth: 0 },
    approval: { mode: "boundary-only" },
    deliver: { mode: "chips", exchange_folder: join(data, "exchange") },
    operator_files: { allow_mailbox_approvals: false, log_retention_days: 30 },
    context: { soft_pct: .75, summary_pct: .95, accounting: "estimated" },
    memory: { enabled: false, dir: join(data, "memory"), max_tokens: 1500 },
    tools: { read_file: { default_limit: 16384, max_limit: 65536 }, attachments: { max_bytes: 8388608 }, list_dir: { max_entries: 300, ignore: [".git"] }, grep: { max_matches: 50, max_line_chars: 200 }, shell: { operator_commands: [] }, fetch: { timeout_s: 20, max_bytes: 2097152, max_redirects: 5, default_limit: 16384, max_limit: 65536, allow_domains: [], deny_domains: [], allow_internal_hosts: [] }, find_files: { skip_roots: [] } },
    shell: { command: ["powershell", "-NoProfile", "-NonInteractive", "-Command"], timeout_s: 60, max_timeout_s: 600, max_output_lines_head: 60, max_output_lines_tail: 40, file_routing_guard: true, operator_context: false, operator_context_idle_timeout_minutes: 20, service_account: { enabled: false, account: "agentb-svc", domain: "." }, deny: [] },
    signing: { thumbprint: "", timestamp_url: "http://timestamp.digicert.com" },
  };
}

// start brings up the harness. `reachable: false` leaves the model port unbound,
// which is what the operator had. `routeFailures` is a set of API path prefixes
// the browser context fails, so a scenario can break `/api/plan` alone.
export async function start({ exe, appRoot, data, reachable = true, viewport = { width: 1250, height: 975 }, readyTimeout = 15000, agents = [] }) {
  const dataRoot = resolve(data);
  const workspace = join(dataRoot, "workspace");
  await mkdir(workspace, { recursive: true });

  const modelPort = await freePort();
  let model = null;
  if (reachable) {
    model = createServer(async (request, response) => {
      if (request.url === "/props") return void response.end(JSON.stringify({ server: "ui-harness", n_ctx: 32768 }));
      if (request.url === "/v1/models") return void response.end(JSON.stringify({ data: [{ id: "ui-harness" }] }));
      if (request.url === "/tokenize") return void response.end(JSON.stringify({ tokens: [1] }));
      if (request.url === "/apply-template") return void response.end(JSON.stringify({ prompt: "ui harness" }));
      for await (const _chunk of request) { /* drain */ }
      if (request.url !== "/v1/chat/completions") { response.statusCode = 404; return void response.end(); }
      response.writeHead(200, { "Content-Type": "text/event-stream" });
      response.write(`data: ${JSON.stringify({ choices: [{ delta: { content: "ui harness reply" }, finish_reason: null }] })}\n\n`);
      response.end(`data: ${JSON.stringify({ choices: [{ delta: {}, finish_reason: "stop" }], usage: { prompt_tokens: 2, completion_tokens: 3 } })}\n\ndata: [DONE]\n\n`);
    });
    await new Promise((done) => model.listen(modelPort, "127.0.0.1", done));
  }

  const appPort = await freePort();
  const configPath = join(dataRoot, "harness.json");
  await writeFile(configPath, JSON.stringify(configFor({ appPort, modelPort, data: dataRoot, workspace, agents }), null, 2));

  let stderr = "";
  const app = spawn(resolve(exe), ["-config", configPath, "-app-root", resolve(appRoot), "-data-root", dataRoot], { windowsHide: true, stdio: ["ignore", "ignore", "pipe"] });
  app.stderr.on("data", (chunk) => { stderr += String(chunk); });

  const base = `http://127.0.0.1:${appPort}`;
  const getState = async () => {
    const response = await fetch(`${base}/api/state`);
    assert.equal(response.status, 200);
    return response.json();
  };
  // Restoring many retained journals takes longer than a bare start, so the
  // caller can wait longer; the stderr is carried into the failure either way.
  const initial = await waitFor(getState, "the harness became ready", readyTimeout).catch((error) => {
    throw new Error(`${error.message}; stderr: ${stderr.slice(-800)}`);
  });

  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const context = await browser.newContext({ viewport });

  return {
    base, appPort, modelPort, app, context, browser, initial, getState, dataRoot,
    stderr: () => stderr,
    // failRoute breaks one API path for every page in this context, which is
    // how 2gf reproduces "with `/api/plan` failing" without touching the server.
    async failRoute(pattern, status = 500) {
      await context.route(pattern, (route) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ title: "failed for the scenario" }) }));
    },
    async stop() {
      await browser.close().catch(() => {});
      await stopChild(app);
      if (model) {
        model.closeAllConnections?.();
        await new Promise((done) => model.close(done));
      }
    },
  };
}
