import assert from "node:assert/strict";
import { callServiceFlowReadout, callServiceKey, callServiceStatus } from "./call-service-display.js";

const args = { service: "broker", method: "post", path: "deploy/jobs", headers: { Accept: "[redacted]" } };
assert.equal(callServiceKey(args), "broker POST deploy/jobs");
assert.equal(callServiceStatus("call_service", { status: 409 }), "HTTP 409");
assert.equal(callServiceStatus("fetch_url", { status: 200 }), "");

const session = {
  activity: { stage: "execute", active_tool: "" },
  chat: [{ type: "tool", name: "call_service", args, result: { status: 202, ms: 37 } }],
};
assert.equal(callServiceFlowReadout(session, (ms) => `${ms} ms`), "broker POST deploy/jobs · HTTP 202 · 37 ms");
assert.equal(callServiceFlowReadout({ ...session, activity: { stage: "append" } }, String), "");
