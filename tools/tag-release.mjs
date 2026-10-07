// The release tag is written here and nowhere else. rel-1.42.0 pushed v1.42.0 on a
// commit whose build still reported v1.41.0; the ruleset keeps a pushed tag, so that
// name was spent. The tag command is refused unless the tree's buildinfo and the
// installer's display version both equal the tag being written — a gate, not a habit.
//
// Usage: node tools/tag-release.mjs vX.Y.Z

import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";

const repo = path.resolve(import.meta.dirname, "..");

export function releaseIdentity(root = repo) {
  const buildinfo = fs.readFileSync(path.join(root, "internal", "buildinfo", "buildinfo.go"), "utf8").match(/\bTag\s*=\s*"([^"]+)"/)?.[1] ?? "";
  const display = fs.readFileSync(path.join(root, "scripts", "install-Agent_b.ps1"), "utf8").match(/\$displayVersion = '([^']+)'/)?.[1] ?? "";
  return { buildinfo, display };
}

export function tagRefusal(tag, identity) {
  if (!/^v\d+\.\d+\.\d+$/.test(tag)) return `${tag} is not a vX.Y.Z tag`;
  if (identity.buildinfo !== tag) return `internal/buildinfo reports ${identity.buildinfo || "nothing"}, not ${tag}`;
  if (`v${identity.display}` !== tag) return `the installer displays ${identity.display || "nothing"}, not ${tag.slice(1)}`;
  return "";
}

export function telemetryVectors(root = repo) {
  const document = fs.readFileSync(path.join(root, "docs", "telemetry-trace.md"), "utf8");
  return [...document.matchAll(/```json vector:[^\r\n]+\r?\n([\s\S]*?)```/g)]
    .map(match => JSON.parse(match[1]))
    .filter(vector => typeof vector.type === "string");
}

export async function releaseAfterTelemetry(thenTag, options = {}) {
  const endpoint = options.endpoint ?? "https://broker.agentb.app/v1/telemetry";
  const vectors = options.vectors ?? telemetryVectors(options.root);
  const report = options.report ?? console.log;
  const post = options.fetch ?? fetch;
  for (const event of vectors) {
    let response;
    try {
      response = await post(endpoint, {
        method: "POST",
        headers: { "content-type": "application/json", "X-Agentb-Synthetic": "1" },
        body: JSON.stringify({
          schema: 1,
          install_id: "00000000-0000-4000-8000-000000000000",
          sent_at: event.at,
          agent_version: releaseIdentity(options.root).buildinfo,
          os: "windows",
          events: [event],
        }),
      });
    } catch (error) {
      report(`telemetry intake: not exercised: external; reason: ${error.cause?.code ?? error.message}`);
      return thenTag();
    }
    const text = await response.text();
    let answer;
    try { answer = JSON.parse(text); } catch { answer = {}; }
    if (!response.ok) throw new Error(`telemetry intake refused ${event.type}: HTTP ${response.status} ${text}`);
    if (!("stored" in answer) || !("dropped" in answer)) {
      report(`telemetry intake ${event.type}: HTTP ${response.status}; storage not proved: the broker's answer does not say`);
      continue;
    }
    if (answer.stored !== 1 || !Array.isArray(answer.dropped) || answer.dropped.length !== 0) {
      throw new Error(`telemetry intake refused ${event.type}: HTTP ${response.status} ${text}`);
    }
    report(`telemetry intake ${event.type}: HTTP ${response.status} stored=1 dropped=[]`);
  }
  return thenTag();
}

if (process.argv[1] && path.resolve(process.argv[1]) === path.resolve(import.meta.filename)) {
  const tag = process.argv[2] || "";
  const git = (...args) => execFileSync("git", ["-C", repo, ...args]).toString().trim();
  let refusal = tagRefusal(tag, releaseIdentity());
  if (!refusal && git("status", "--porcelain", "--untracked-files=normal")) refusal = "the tree is dirty";
  if (refusal) {
    console.error(`TAG REFUSED: ${refusal}`);
    process.exit(1);
  }
  await releaseAfterTelemetry(() => git("tag", "-a", tag, "-m", `Agent_b ${tag}`));
  console.log(`TAGGED ${tag} at ${git("rev-parse", "HEAD")}`);
}
