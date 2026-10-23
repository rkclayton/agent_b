import { createServer } from "node:http";

// 2wi: a local stand-in model server for the compaction proof's release gate. It speaks the two
// requests the harness sends (internal/llm/client.go): a streamed chat completion for a turn and a
// plain one for the hand-off, whose last message is the hand-off instruction (internal/context/one_rule.go).
// After each user message it calls read_file on the next numbered file `calls` times, then answers in
// prose of `replyTokens`; its counts are the harness's own estimate, ten tokens per 36 characters.
export const stubModel = "stub";
export const stubHandOff = "DONE:\nRead the numbered files so far.\nNEXT:\nKeep reading them in filename order.\nFILES CHANGED:\nnotes.md\nOPEN QUESTIONS:\nnone";
const tokens = (text) => Math.ceil([...String(text ?? "")].length * 10 / 36);
const text = (content) => Array.isArray(content) ? content.map((part) => part?.text || "").join("") : String(content ?? "");

export function promptTokens(body) {
  const messages = body.messages || [];
  return messages.reduce((sum, one) => sum + 4 + tokens(text(one.content)) + (one.tool_calls || []).reduce((all, call) => all + tokens(call.function?.arguments), 0), 0) + tokens(JSON.stringify(body.tools || []));
}

// stubTurn decides one answer. `calls` lists, per user message in order, how many reads follow it;
// the last entry repeats. A read names the next file the stub has not yet asked for, so no path repeats.
export function stubTurn(body, state, { calls = [3], replyTokens = 40 } = {}) {
  const messages = body.messages || [], last = messages.at(-1) || {};
  if (last.role === "user" && text(last.content).startsWith("Write a compact hand-off")) return { content: stubHandOff };
  if (!body.stream) return { content: "Proof chat" };
  if (last.role === "user") { state.users = (state.users || 0) + 1; state.left = calls[Math.min(state.users, calls.length) - 1]; }
  if (state.left > 0 && (last.role === "user" || last.role === "tool")) {
    state.left--; state.read = (state.read || 0) + 1;
    return { tool_call: { id: `call_${state.read}`, type: "function", function: { name: "read_file", arguments: JSON.stringify({ path: `${String(state.read).padStart(4, "0")}.txt` }) } } };
  }
  const opening = `Read ${state.read || 0} numbered files so far.`;
  return { content: `${opening} ${"stand-in words ".repeat(Math.max(0, Math.ceil((replyTokens - tokens(opening)) * 36 / 10 / 15)))}`.trim() };
}

function send(response, status, value) {
  response.writeHead(status, { "Content-Type": "application/json" }); response.end(JSON.stringify(value));
}

export async function startStubModel(options = {}) {
  const state = {}, requests = [];
  const server = createServer((request, response) => {
    let raw = "";
    request.on("data", (part) => raw += part);
    request.on("end", async () => {
      if (options.delayMs) await new Promise((done) => setTimeout(done, options.delayMs));
      const path = new URL(request.url, "http://stub").pathname;
      if (request.method === "GET" && (path === "/v1/models" || path === "/models")) return send(response, 200, { object: "list", data: [{ id: stubModel, object: "model" }] });
      if (request.method !== "POST" || path !== "/v1/chat/completions") return send(response, 404, { error: { message: "not served by the stand-in model" } });
      const body = JSON.parse(raw || "{}"), turn = stubTurn(body, state, options), prompt = promptTokens(body);
      const completion = tokens(turn.content) + tokens(turn.tool_call?.function.arguments), usage = { prompt_tokens: prompt, completion_tokens: completion, total_tokens: prompt + completion };
      const finish = turn.tool_call ? "tool_calls" : "stop";
      requests.push({ stream: Boolean(body.stream), last: body.messages?.at(-1)?.role, finish, prompt_tokens: prompt });
      if (!body.stream) return send(response, 200, { id: "stub", object: "chat.completion", model: stubModel, choices: [{ index: 0, message: { role: "assistant", content: turn.content, ...(turn.tool_call ? { tool_calls: [turn.tool_call] } : {}) }, finish_reason: finish }], usage });
      response.writeHead(200, { "Content-Type": "text/event-stream" });
      const chunk = (value) => response.write(`data: ${JSON.stringify({ id: "stub", object: "chat.completion.chunk", model: stubModel, ...value })}\n\n`);
      chunk({ choices: [{ index: 0, delta: turn.tool_call ? { role: "assistant", tool_calls: [{ index: 0, ...turn.tool_call }] } : { role: "assistant", content: turn.content }, finish_reason: null }] });
      chunk({ choices: [{ index: 0, delta: {}, finish_reason: finish }] });
      chunk({ choices: [], usage });
      response.end("data: [DONE]\n\n");
    });
  });
  await new Promise((done) => server.listen(0, "127.0.0.1", done));
  return { url: `http://127.0.0.1:${server.address().port}`, requests, close: () => new Promise((done) => server.close(done)) };
}
