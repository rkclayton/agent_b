#!/usr/bin/env node

import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdir, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";

const argv = process.argv.slice(2);
const args = Object.fromEntries(Array.from({ length: Math.floor(argv.length / 2) }, (_, index) => [argv[index * 2].replace(/^--/, ""), argv[index * 2 + 1]]));
for (const name of ["exe", "app-root", "data", "workspace", "server", "model", "evidence"]) assert.ok(args[name], `missing --${name}`);

const sleep = (ms) => new Promise((resolvePromise) => setTimeout(resolvePromise, ms));
const portProbe = createServer();
await new Promise((done) => portProbe.listen(0, "127.0.0.1", done));
const port = portProbe.address().port;
await new Promise((done) => portProbe.close(done));

const data = resolve(args.data);
const workspace = resolve(args.workspace);
const evidence = resolve(args.evidence);
await mkdir(data, { recursive: true });
await mkdir(workspace, { recursive: true });
await mkdir(evidence, { recursive: true });
const profile = {
  id: "real-template", label: "Real template acceptance", base_url: args.server, model: args.model,
  credential: "", request_timeout_s: 120, probe_mode: "off", attachment_handling: "extract",
  sampling: { thinking: { temperature: 0, top_p: 1, top_k: 1, min_p: 0, presence_penalty: 0, repeat_penalty: 1 }, nonthinking: { temperature: 0, top_p: 1, top_k: 1, min_p: 0, presence_penalty: 0, repeat_penalty: 1 } },
  reasoning: { control: "chat_template_kwargs", enabled: false, effort: "medium", valid_efforts: [], preserve: false },
  context: { n_ctx: 262144, reserve_output: 4096 }, system_prompt_override: "Reply briefly.",
  capabilities: { server: "llama.cpp", props: true, n_ctx: 262144, tokenize: true, apply_template: true, apply_template_tools: true, streaming: true, tool_calls: true, grammar_constrained: false, cached_tokens: true, timings: true, prompt_progress: false, document_input: false, image_input: false, reasoning_control: "chat_template_kwargs", valid_efforts: [], overflow_behavior: "error", findings: ["real-template acceptance"], probed_at: new Date().toISOString() },
};
const config = {
  config_version: 7, listen: `127.0.0.1:${port}`, workspace, log_dir: join(data, "logs"),
  servers: [profile], services: {}, agents: [{ name: "Real template", b: profile.id, toolset: [] }],
  roles: { main: profile.id, aux: "" }, context: { soft_pct: 0.75, summary_pct: 0.85, accounting: "exact" },
  memory: { enabled: false, dir: join(data, "memory"), max_tokens: 1500 },
  shell: { command: ["powershell", "-NoProfile", "-NonInteractive", "-Command"], timeout_s: 60, max_timeout_s: 600, max_output_lines_head: 60, max_output_lines_tail: 40, file_routing_guard: true, operator_context: false, operator_context_idle_timeout_minutes: 20, service_account: { enabled: false, account: "agentb-svc", domain: "." }, deny: [] },
};
await writeFile(join(data, "harness.json"), JSON.stringify(config, null, 2));

const app = spawn(resolve(args.exe), ["-config", join(data, "harness.json"), "-app-root", resolve(args["app-root"]), "-data-root", data], { windowsHide: true, stdio: ["ignore", "pipe", "pipe"] });
let stderr = "";
app.stderr.on("data", (chunk) => { stderr += String(chunk); });
const base = `http://127.0.0.1:${port}`;
const json = async (path, init) => {
  const response = await fetch(base + path, init);
  const body = await response.text();
  assert.ok(response.ok, `${path} HTTP ${response.status}: ${body}`);
  return body ? JSON.parse(body) : {};
};
const state = async () => json("/api/state");
const waitFor = async (predicate, label, timeout = 180000) => {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    try {
      const value = await state();
      if (predicate(value)) return value;
    } catch {}
    await sleep(100);
  }
  throw new Error(`${label} timed out: ${stderr}`);
};

try {
  let current = await waitFor((value) => value?.build, "disposable harness startup", 30000);
  const created = await json("/api/sessions", { method: "POST", headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": current.mutation_token }, body: JSON.stringify({ agent_id: "real-template" }) });
  const sessionID = created.session.id;
  const send = async (text) => {
    current = await state();
    await json("/api/message", { method: "POST", headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": current.mutation_token }, body: JSON.stringify({ session_id: sessionID, text }) });
    return waitFor((value) => value.sessions?.[sessionID]?.run?.status === "idle" && value.sessions[sessionID].messages?.at(-1)?.role === "assistant", `answer to ${JSON.stringify(text)}`);
  };
  await send("Reply with exactly: hello");
  current = await send("good morning");
  const session = current.sessions[sessionID];
  const budget = session.budget;
  assert.ok(session.messages.at(-1).content, "second turn produced no assistant answer");
  assert.ok(budget?.mode === "exact" || budget?.mode === "estimated", `missing budget row: ${JSON.stringify(budget)}`);
  assert.notEqual(session.run.last_stop_reason, "model_error", JSON.stringify(session.run));
  const result = { schema: 1, server: args.server, model: args.model, build: current.build, session_id: sessionID, run: session.run, budget, messages: session.messages.map(({ role, content }) => ({ role, content: String(content ?? "").slice(0, 200) })) };
  await writeFile(join(evidence, "result.json"), JSON.stringify(result, null, 2));
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
} finally {
  if (app.exitCode === null) process.kill(app.pid);
  await Promise.race([new Promise((done) => app.once("exit", done)), sleep(10000)]);
}
