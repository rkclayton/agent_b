// Item 2gr, v1.2.5/W3 — what the etching costs the text, measured.
//
// The overlay is one layer over the whole window at a few percent, so it
// lightens one edge of a surface and darkens the other. That is a real cost to
// text contrast, and v1.0.0 measured it once at the low default: -1.3% median,
// -5.2% on the worst tile. The operator has now judged that default "about
// twice what he wants", so the default halves - and the same measurement has to
// be made again rather than assumed to have halved with it.
//
// Method: render one page at each fade, tile it, and for every tile that holds
// text compare the tile's contrast RANGE (its lightest against its darkest
// pixel, as a WCAG ratio) with the same tile at fade 0. The change is reported
// as a percentage of the unetched ratio, median and worst.
//
//   node scripts/etching-contrast.mjs --exe <exe> --app-root <dir> --data <dir> --evidence <dir>
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { start, waitFor } from "./ui-harness.mjs";
import { decodePNG } from "./png.mjs";

const argv = process.argv.slice(2);
const args = Object.fromEntries(Array.from({ length: argv.length / 2 }, (_, index) => [argv[index * 2].replace(/^--/, ""), argv[index * 2 + 1]]));
for (const name of ["exe", "app-root", "data", "evidence"]) assert.ok(args[name], `missing --${name}`);
const evidence = resolve(args.evidence);
await mkdir(evidence, { recursive: true });

const TILE = 32;
const luminance = (r, g, b) => {
  const channel = (value) => {
    const v = value / 255;
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
};

// tileRatios returns one contrast ratio per tile: the lightest pixel against
// the darkest. A tile of flat background has a ratio near 1 and is not text.
function tileRatios(image) {
  const ratios = [];
  for (let top = 0; top + TILE <= image.height; top += TILE) {
    for (let left = 0; left + TILE <= image.width; left += TILE) {
      let lightest = 0;
      let darkest = 1;
      for (let y = top; y < top + TILE; y++) {
        for (let x = left; x < left + TILE; x++) {
          const at = (y * image.width + x) * 4;
          const value = luminance(image.data[at], image.data[at + 1], image.data[at + 2]);
          if (value > lightest) lightest = value;
          if (value < darkest) darkest = value;
        }
      }
      ratios.push({ left, top, ratio: (lightest + 0.05) / (darkest + 0.05) });
    }
  }
  return ratios;
}

const harness = await start({ exe: args.exe, appRoot: args["app-root"], data: args.data, reachable: true });
const measurements = [];
try {
  const page = await harness.context.newPage();
  const created = await page.request.post(`${harness.base}/api/sessions`, { data: { agent_id: "ui" }, headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": harness.initial.mutation_token } });
  assert.ok(created.ok(), `creating a chat: ${created.status()}`);
  const sessionID = (await created.json()).id;
  await page.goto(`${harness.base}/chat?session=${sessionID}`);
  await waitFor(async () => page.evaluate(() => !!document.querySelector("#chat-task")), "the chat drew");
  // Text on every surface the overlay crosses, so the tiles that matter are
  // text tiles rather than empty background.
  await page.evaluate(() => {
    const log = document.getElementById("chat-log");
    log.hidden = false;
    log.innerHTML = Array.from({ length: 24 }, (_, index) => `<div class="chat-entry chat-agent"><span class="chat-speaker">agent_b</span><div class="chat-response-prose">Sample line ${index} for measuring what the etching costs the text it lies over.</div></div>`).join("");
  });

  const fades = ["0", "0.165", "0.33"];
  const images = {};
  for (const fade of fades) {
    await page.evaluate((value) => { document.documentElement.style.setProperty("--etch-fade", value); }, fade);
    await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))));
    const file = join(evidence, `etching-${fade.replace(".", "_")}.png`);
    await page.screenshot({ path: file, animations: "disabled" });
    images[fade] = tileRatios(decodePNG(await page.screenshot({ animations: "disabled" })));
  }

  const base = images["0"];
  for (const fade of fades.slice(1)) {
    const deltas = images[fade]
      .map((tile, index) => ({ ...tile, base: base[index].ratio, change: (tile.ratio - base[index].ratio) / base[index].ratio }))
      // Only tiles that carry text: a flat tile has nothing to lose.
      .filter((tile) => tile.base > 1.5)
      .sort((a, b) => a.change - b.change);
    const median = deltas.length ? deltas[Math.floor(deltas.length / 2)].change : 0;
    const worst = deltas.length ? deltas[0].change : 0;
    measurements.push({
      fade,
      text_tiles: deltas.length,
      median_change_percent: +(median * 100).toFixed(2),
      worst_change_percent: +(worst * 100).toFixed(2),
    });
    process.stdout.write(`fade ${fade}: ${deltas.length} text tiles · median ${(median * 100).toFixed(2)}% · worst ${(worst * 100).toFixed(2)}%\n`);
  }
  await writeFile(join(evidence, "etching-contrast.json"), `${JSON.stringify({ tile: TILE, measurements }, null, 1)}\n`);
} finally {
  await harness.stop();
}
