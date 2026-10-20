import { mkdir, readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { expect, test } from "@playwright/test";
import { decodePNG } from "../png.mjs";
import { chatScaleMatrix } from "../layout-gate.mjs";

const webRoot = fileURLToPath(new URL("../../web/", import.meta.url));

const appCSS = await readFile(new URL("../../web/css/app.css", import.meta.url), "utf8");
const tokensCSS = await readFile(new URL("../../web/css/tokens.css", import.meta.url), "utf8");
const chatCSS = await readFile(new URL("../../web/css/chat.css", import.meta.url), "utf8");
const chatJS = await readFile(new URL("../../web/js/chat.js", import.meta.url), "utf8");
const indexHTML = await readFile(new URL("../../web/index.html", import.meta.url), "utf8");

test("the continued turn sentence is visible 2vt", async ({ page }) => {
  await mkdir("logs/evidence/rel-1.62.136/captures", { recursive:true }); await page.route("http://continued.test/**", (route) => new URL(route.request().url()).pathname === "/" ? route.fulfill({ contentType:"text/html; charset=utf-8", body:`<style>${tokensCSS}${chatCSS}body{background:var(--well);color:var(--ink);padding:24px}</style><button class="chat-step-summary"><span id="sentence"></span><span class="chat-step-caret">›</span></button><script type="module">import {responseSummary,responseSummarySentence} from '/js/chat-response-groups.js';sentence.textContent=responseSummarySentence(responseSummary([{type:'notice',event:{type:'model.retry',data:{reason:'announced_action'}}}]));</script>` }) : route.fulfill({ path:webRoot + new URL(route.request().url()).pathname.slice(1) }));
  await page.goto("http://continued.test/"); await expect(page.locator("#sentence")).toHaveText("Continued once");
  for (const width of [304,1280,1920]) { await page.setViewportSize({width,height:254}); expect(await page.locator(".chat-step-summary").evaluate((node) => node.scrollWidth <= node.clientWidth && node.getBoundingClientRect().right <= innerWidth)).toBe(true); await page.screenshot({path:`logs/evidence/rel-1.62.136/captures/continued-once-${width}.png`}); }
});

test("attachment page progress and remove remain visible on the bubble", async ({ page }) => {
  expect(chatJS).toContain("page ${upload.page} of ${upload.total}");
  expect(chatJS).toContain('/api/attachments/stop');
  await page.setViewportSize({ width: 320, height: 180 });
  await page.setContent(`<!doctype html><style>:root{--ink:#D8DDE3;--well:#15181C;--trace:#F2B233;--alarm:#E4624F;--sans:sans-serif}${chatCSS}</style>
    <div class="chat-pending-attachments"><span class="chat-pending-file attachment-bubble chat-bubble"><span class="attachment-bubble-type">PDF</span><span class="attachment-bubble-words"><span>phone-scan.pdf</span><span>page 10 of 60</span></span><button class="attachment-bubble-remove" aria-label="Remove phone-scan.pdf">×</button></span></div>`);
  await expect(page.getByText("page 10 of 60")).toBeVisible();
  await expect(page.getByRole("button", { name: "Remove phone-scan.pdf" })).toBeVisible();
  const shot = await page.screenshot();
  expect(shot.length, "the off-screen attachment bubble screenshot is empty").toBeGreaterThan(0);
  expect(await page.locator(".chat-pending-file").evaluate((node) => node.getBoundingClientRect().right <= innerWidth)).toBe(true);
});

test("2u1 real chat bubbles preview remove stop and send only what remains", async ({ browser }) => {
  const png = await readFile(new URL("../../web/assets/plan-mark.png", import.meta.url));
  const id = "chat-2u1", uploads = [
    { path: "attachments/named.png", bytes: png.length, sha256: "1".repeat(64), kind: "image", sidecar: "attachments/named.png.ocr.txt", note: "full image reading note" },
    { path: "attachments/pasted-attachment.png", bytes: png.length, sha256: "2".repeat(64), kind: "image", sidecar: "attachments/pasted-attachment.png.ocr.txt", note: "full pasted image reading note" },
    { path: "attachments/unreadable.bin", bytes: 3, sha256: "3".repeat(64), kind: "binary", note: "binary — this connection cannot read it" },
  ];
  const state = { sessions: { [id]: { schema_version:1, cursor:{generation:"2u1.jsonl",offset:1}, complete:true, id, label:"Attachment proof", agent_id:"agent_b", role:"b", created_at:"2026-10-12T00:00:00Z", run:{status:"idle"}, tools:[], messages:[], budget:{}, activity:{completed_stages:[]}, timeline:[], runnable:true, closed:false, chat:[{ type:"user", key:"message:sent", text:"Sent picture", attachments:[{ path:"attachments/sent.png", bytes:png.length, sha256:"4".repeat(64), kind:"image" }] }] } }, connections:[{ id:"local", reads_images:false, capabilities:{} }], config:{ agents:[{name:"agent_b",b:"fixture"}], connections:[] }, flow:{stages:[],edges:[]}, tools:[], plans:[], profiles:{active:"",names:[]}, build:{} };
  state.sessions[id].connection_id = "local";
  const context = await browser.newContext({ viewport:{width:1280,height:860} });
  await context.addInitScript(({ state, id }) => {
    sessionStorage.setItem("agentb.selection", JSON.stringify({agent_id:"agent_b",session_id:id,surface:{kind:"chat",key:id}}));
    Object.defineProperty(globalThis.crypto,"randomUUID",{value:()=>"00000000-0000-4000-8000-000000000001"});
    class Events { constructor(){this.listeners=new Map();globalThis.__fixtureEvents=this;setTimeout(()=>this.emit("snapshot",{type:"snapshot",data:state}))} addEventListener(type,fn){this.listeners.set(type,[...(this.listeners.get(type)||[]),fn])} emit(type,value){const event={data:JSON.stringify(value)};for(const fn of this.listeners.get(type)||[])fn(event);if(type==="message")this.onmessage?.(event)} close(){} }
    globalThis.EventSource=Events;
  }, { state, id });
  const page = await context.newPage(); let uploadIndex = 0, stopPDF, sentBody, opened = false;
  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url()), path = url.pathname;
    if (path === "/chat") return route.fulfill({contentType:"text/html",body:indexHTML});
    if (path.startsWith("/api/files/")) return route.fulfill({contentType:"image/png",body:png});
    if (path === "/api/open-file") { opened = true; return route.fulfill({contentType:"application/json",body:"{}"}); }
    if (path === "/api/message") { sentBody = route.request().postDataJSON(); return route.fulfill({contentType:"application/json",body:"{}"}); }
    if (path === "/api/attachments/stop") { stopPDF?.(); return route.fulfill({contentType:"application/json",body:"{}"}); }
    if (path === "/api/attachments" && route.request().method() === "POST") {
      const index = uploadIndex++;
      if (index < uploads.length) return route.fulfill({contentType:"application/json",body:JSON.stringify(uploads[index])});
      const raw = route.request().postDataBuffer().toString("latin1"), uploadID = /name="upload_id"\r?\n\r?\n([^\r\n]+)/.exec(raw)?.[1];
      if (!uploadID) throw new Error("fixture could not read the PDF upload id");
      await page.evaluate(({id,uploadID}) => globalThis.__fixtureEvents.emit("message",{type:"attachment.ocr_progress",session_id:id,data:{session_id:id,upload_id:uploadID,page:3,total:12}}), {id,uploadID});
      await new Promise((resolve) => { stopPDF = resolve; });
      return route.fulfill({status:499,contentType:"application/json",body:'{"error":"stopped"}'});
    }
    if (path.startsWith("/api/")) return route.fulfill({contentType:"application/json",body:"{}"});
    return route.fulfill({path:webRoot + path.replace(/^\/static\//,"")});
  });
  await page.goto(`http://bubble-2u1.test/chat?setup=skip&session=${id}`, {waitUntil:"domcontentloaded"});
  await expect(page.getByText("Sent picture")).toBeVisible();
  await page.locator('.chat-user .attachment-bubble-name').click(); expect(opened).toBe(true);
  const picker = page.locator("#chat-file-picker");
  await picker.setInputFiles({name:"named.png",mimeType:"image/png",buffer:png}); await expect(page.getByText("named.png")).toBeVisible();
  await page.evaluate((bytes) => { const file=new File([new Uint8Array(bytes)],"",{type:"image/png"}), transfer=new DataTransfer();transfer.items.add(file);document.body.dispatchEvent(new DragEvent("drop",{bubbles:true,dataTransfer:transfer})); }, [...png]);
  await expect(page.getByText(/Pasted image · \d+ × \d+/)).toBeVisible();
  await picker.setInputFiles({name:"unreadable.bin",mimeType:"application/octet-stream",buffer:Buffer.from("bad")}); await expect(page.locator("#chat-attachments").getByText("this model cannot read it")).toBeVisible();
  await picker.setInputFiles({name:"pages.pdf",mimeType:"application/pdf",buffer:Buffer.from("%PDF")}); await expect(page.getByText("page 3 of 12")).toBeVisible();
  const styles = await page.evaluate(() => [document.querySelector('.chat-user > .chat-content'),document.querySelector('#chat-attachments .attachment-bubble'),document.querySelector('.chat-user .attachment-bubble')].map((node)=>{const s=getComputedStyle(node);return {border:s.border,top:s.borderTopLeftRadius,shadow:s.boxShadow,bottomRight:s.borderBottomRightRadius}}));
  expect(styles.map(({border,top,shadow})=>({border,top,shadow}))).toEqual([0,1,2].map(()=>({border:styles[0].border,top:styles[0].top,shadow:styles[0].shadow}))); expect(styles[0].bottomRight).toBe("3px");
  const forbidden = await page.locator("body").innerText(); expect(forbidden).not.toMatch(/OCR|untrusted|layout|extract|\b\d+ bytes\b/i);
  await mkdir("logs/evidence/rel-1.62.90/captures",{recursive:true});
  for (const [width,height] of [[1280,860],[1920,1080]]) { await page.setViewportSize({width,height}); await page.screenshot({path:`logs/evidence/rel-1.62.90/captures/bubbles-${width}x${height}.png`,fullPage:true}); }
  await page.getByRole("button",{name:"Remove named.png"}).click(); await expect(page.getByText("named.png")).toHaveCount(0); await expect(page.locator("#chat-attachments .attachment-bubble")).toHaveCount(3);
  await page.getByRole("button",{name:"Remove pages.pdf"}).click(); await expect(page.locator("#chat-attachments .attachment-bubble")).toHaveCount(2);
  await page.fill("#chat-task","Send the rest"); await page.locator("#chat-send").click(); await expect.poll(()=>sentBody).not.toBeUndefined();
  expect(sentBody.attachments.map((item)=>item.path)).toEqual(["attachments/pasted-attachment.png","attachments/unreadable.bin"]);
  await context.close();
});

// The standing UI contract, in the operator's words: "nothing scrolls inside
// something that already scrolls — the chat transcript scrolls because it must, and
// a top-level settings page scrolls when its content genuinely needs it, but no
// panel, list or fold inside them gets its own scrollbar."
//
// Item 2l7 (f) asks for exactly this assertion over the Activity page, which is the
// adopted #activity-panel inside the Settings sheet.
function activityMarkup() {
  const start = indexHTML.indexOf('<div id="activity-panel"');
  // The end is the NEXT thing in the holder, whatever it is. Item 2no put the Plan's
  // own markup after this panel, and a slice that ran to the end of the document
  // dragged the Plan's textarea in and reported it as an Activity offender.
  const boundaries = ['<!-- Item 2no', '<div id="plan-panel"', '<div id="dissolved-sources"']
    .map((marker) => indexHTML.indexOf(marker, start))
    .filter((index) => index > start);
  const end = boundaries.length ? Math.min(...boundaries) : indexHTML.indexOf("</div>\n</body>", start);
  return indexHTML.slice(start, end > start ? end : indexHTML.length);
}

for (const width of [1280, 760]) {
  test(`nothing inside the Activity page scrolls independently at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.setContent(`<!doctype html><html><head><style>
      :root { --bezel:#2A2E35; --well:#15181C; --ink:#D8DDE3; --mute:#7D8794; --trace:#F2B233; --alarm:#E4624F;
        --accent-plan:#5AC8FA; --sans:sans-serif; --mono:monospace;
        --s1:4px; --s2:8px; --s3:12px; --s4:16px; --s6:24px; --s8:32px; --app-header-height:32px; --agent-tab-width:118px; --etch-fade:.165; }
      ${appCSS}
    </style></head><body>
      <div id="settings-page" class="settings-page">
        <div class="settings-layout"><div class="settings-content">${activityMarkup()}</div></div>
      </div>
    </body></html>`);

    const offenders = await page.evaluate(() => {
      const page = document.querySelector(".settings-content");
      const scrolls = (node) => {
        const style = getComputedStyle(node);
        return ["overflow", "overflow-y"].some((property) => ["auto", "scroll"].includes(style.getPropertyValue(property)));
      };
      return [...page.querySelectorAll("*")].filter(scrolls).map((node) => {
        const id = node.id ? `#${node.id}` : "";
        return `${node.tagName.toLowerCase()}${id}.${[...node.classList].join(".")}`;
      });
    });
    expect(offenders, "these elements inside the Activity page scroll on their own").toEqual([]);
  });
}

// Controls are the sheet's normal size; nothing oversized. The row actions 2l5 adds
// are the smallest controls on the sheet and must not exceed a normal row's height.
test("the connection row actions are the sheet's normal control size", async ({ page }) => {
  await page.setContent(`<!doctype html><html><head><style>
    :root { --bezel:#2A2E35; --well:#15181C; --ink:#D8DDE3; --mute:#7D8794; --alarm:#E4624F; --mono:monospace; --s1:4px; --s2:8px; }
    ${appCSS}
  </style></head><body>
    <div class="connection-row"><span class="connection-actions">
      <button class="row-action">s</button>
      <button class="row-action test">test</button>
      <button class="row-action">d</button>
      <button class="row-action">x</button>
    </span></div>
  </body></html>`);
  const sizes = await page.$$eval(".row-action", (nodes) => nodes.map((node) => {
    const box = node.getBoundingClientRect();
    return { width: box.width, height: box.height };
  }));
  expect(sizes).toHaveLength(4);
  for (const size of sizes) {
    expect(size.height, "a row action is taller than a settings row").toBeLessThanOrEqual(24);
    expect(size.height).toBeGreaterThan(0);
    expect(size.width).toBeGreaterThan(0);
  }
});

// Item 2u0 removes the picture and explanatory paragraph from the Plan page;
// the compact navigation mark remains and every prepared size still resolves.
test("the Plan page has no intro prose and its navigation artwork resolves", async ({ page }) => {
  const planHTML = await readFile(new URL("../../web/index.html", import.meta.url), "utf8");
  expect(planHTML).not.toContain('class="plan-intro"');
  expect(planHTML).not.toContain("A plan points at one repository");

  // Item 2lm (b) and (f): the nav mark is a size set now, because the strip draws
  // a 12px box and the product runs at more than one device pixel ratio --
  // rel-1.16.0/W0 measured the operator's own display at 1.75x, where that box is
  // 21 physical pixels while every screenshot captures it at 12. Each member is
  // prepared from the full-detail mark rather than reduced from the one above it,
  // and each must resolve as a real image at its own size.
  const assets = ["plan-mark-nav.png", "plan-mark-nav@2x.png", "plan-mark-nav@3x.png", "plan-mark.png"];
  const encoded = await Promise.all(assets.map(async (name) =>
    (await readFile(new URL(`../../web/assets/${name}`, import.meta.url))).toString("base64")));
  await page.setContent(encoded.map((data, index) => `<img id="a${index}" src="data:image/png;base64,${data}">`).join(""));
  const sizes = await page.evaluate(async () => {
    await Promise.all([...document.images].map((image) => image.decode()));
    return [...document.images].map((image) => [image.naturalWidth, image.naturalHeight]);
  });
  expect(sizes).toEqual([[12, 12], [24, 24], [36, 36], [128, 128]]);
});

// Item 2ld. The operator, 2026-09-26: "can we make it where if you grab this
// little part at the top of the chat window in between where you type and it
// displays that you can resize the chat input? and remove the [expand control]".
// Item 2oc: the input row goes all the way to zero while the strip stays as the
// reversible handle, and zero is remembered just like every other height.
test("the composer collapses to zero, remembers it, and opens from the same handle", async ({ page }) => {
  const chatCSS = await readFile(new URL("../../web/css/chat.css", import.meta.url), "utf8");
  const body = `<!doctype html><html><head><style>
    :root { --bezel:#2A2E35; --well:#15181C; --ink:#D8DDE3; --mute:#7D8794; --mono:monospace; }
    body { margin:0 } #wrap { height:600px; display:grid; grid-template-rows: 1fr auto }
    #chat-log { overflow-y:auto } ${chatCSS}
  </style></head><body><div id="wrap">
    <main id="chat-log"><p>transcript</p></main>
    <footer id="chat-composer" class="chat-composer">
      <div id="chat-resize-handle" class="chat-resize-handle" role="separator" title="Drag to resize the message box"></div>
      <div class="chat-composer-row"><div class="chat-input-wrap"><textarea id="chat-input"></textarea><span class="chat-input-actions"><button id="chat-mic" class="composer-control">mic</button><button id="chat-send" class="composer-control">send</button></span></div></div>
    </footer>
  </div>
  <script type="module">
    import { installComposerResize, COMPOSER_MIN } from "/js/composer-resize.js";
    window.COMPOSER_MIN = COMPOSER_MIN;
    installComposerResize({ handle: document.getElementById("chat-resize-handle"), composer: document.getElementById("chat-composer"), input: document.getElementById("chat-input"), log: document.getElementById("chat-log") });
  </script></body></html>`;
  await page.route("**/*", (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/composer.html") return route.fulfill({ contentType: "text/html", body });
    return route.fulfill({ path: webRoot + path.replace(/^\//, "") });
  });
  await page.goto("http://composer.test/composer.html");
  await page.fill("#chat-input", "still here");

  const floor = await page.evaluate(() => window.COMPOSER_MIN);
  expect(floor, "the floor is a true zero").toBe(0);

  // Drag far past the bottom of the window: the clamp, not the pointer, decides.
  const strip = await page.locator("#chat-resize-handle").boundingBox();
  await page.mouse.move(strip.x + 40, strip.y + strip.height / 2);
  await page.mouse.down();
  await page.mouse.move(strip.x + 40, strip.y + strip.height / 2 + 900, { steps: 8 });
  await page.mouse.up();

  const applied = await page.evaluate(() => Number.parseFloat(document.getElementById("chat-composer").style.getPropertyValue("--composer-height")));
  expect(applied, "the composer did not reach its zero floor").toBe(floor);
  // The row and its controls are truly gone, while the typed value is retained.
  await expect(page.locator("#chat-input")).toHaveValue("still here");
  expect((await page.locator("#chat-input").boundingBox()).height).toBe(0);
  expect((await page.locator(".chat-composer-row").boundingBox()).height, "the controls still occupied a row").toBe(0);
  const collapsed = await page.locator("#chat-composer").boundingBox();
  const collapsedStrip = await page.locator("#chat-resize-handle").boundingBox();
  expect(collapsed.y + collapsed.height, "space remained below the handle").toBe(collapsedStrip.y + collapsedStrip.height);
  // The handle is still findable: the strip is still there and still the target.
  const smallest = await page.locator("#chat-resize-handle").boundingBox();
  expect(smallest.height, "the handle disappeared at the smallest size").toBe(4);

  await page.reload();
  expect(await page.evaluate(() => Number.parseFloat(document.getElementById("chat-composer").style.getPropertyValue("--composer-height"))), "zero was not restored after reload").toBe(0);
  expect((await page.locator("#chat-input").boundingBox()).height).toBe(0);
  expect((await page.locator(".chat-composer-row").boundingBox()).height).toBe(0);

  // And it comes back: the drag is reversible from the floor.
  const restoredStrip = await page.locator("#chat-resize-handle").boundingBox();
  await page.mouse.move(restoredStrip.x + 40, restoredStrip.y + restoredStrip.height / 2);
  await page.mouse.down();
  await page.mouse.move(restoredStrip.x + 40, restoredStrip.y + restoredStrip.height / 2 - 100, { steps: 6 });
  await page.mouse.up();
  const back = await page.evaluate(() => Number.parseFloat(document.getElementById("chat-composer").style.getPropertyValue("--composer-height")));
  expect(back, "the composer could not be dragged back up from the floor").toBeGreaterThan(floor);
  await page.fill("#chat-input", "usable again");
  await expect(page.locator("#chat-input")).toHaveValue("usable again");
});

test("dragging the strip resizes the composer, keeps what is typed, and persists", async ({ page }) => {
  const chatCSS = await readFile(new URL("../../web/css/chat.css", import.meta.url), "utf8");
  const body = `<!doctype html><html><head><style>
    :root { --bezel:#2A2E35; --well:#15181C; --ink:#D8DDE3; --mute:#7D8794; --mono:monospace; }
    body { margin:0 } #wrap { height:600px; display:grid; grid-template-rows: 1fr auto }
    #chat-log { overflow-y:auto } ${chatCSS}
  </style></head><body><div id="wrap">
    <main id="chat-log"><p>transcript</p></main>
    <footer id="chat-composer" class="chat-composer">
      <div id="chat-resize-handle" class="chat-resize-handle" role="separator" title="Drag to resize the message box"></div>
      <div class="chat-composer-row"><div class="chat-input-wrap"><textarea id="chat-input"></textarea></div></div>
    </footer>
  </div>
  <script type="module">
    import { installComposerResize } from "/js/composer-resize.js";
    installComposerResize({ handle: document.getElementById("chat-resize-handle"), composer: document.getElementById("chat-composer"), input: document.getElementById("chat-input"), log: document.getElementById("chat-log") });
  </script></body></html>`;
  await page.route("**/*", (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/composer.html") return route.fulfill({ contentType: "text/html", body });
    return route.fulfill({ path: webRoot + path.replace(/^\//, "") });
  });
  await page.goto("http://composer.test/composer.html");

  await page.fill("#chat-input", "a message I am still writing");
  const strip = await page.locator("#chat-resize-handle").boundingBox();
  const before = (await page.locator("#chat-input").boundingBox()).height;

  // Drag the strip UP: the message box grows.
  await page.mouse.move(strip.x + 40, strip.y + strip.height / 2);
  await page.mouse.down();
  await page.mouse.move(strip.x + 40, strip.y + strip.height / 2 - 120, { steps: 6 });
  await page.mouse.up();
  const taller = (await page.locator("#chat-input").boundingBox()).height;
  expect(taller, "dragging up did not make the composer taller").toBeGreaterThan(before + 60);

  // And back DOWN.
  const strip2 = await page.locator("#chat-resize-handle").boundingBox();
  await page.mouse.move(strip2.x + 40, strip2.y + strip2.height / 2);
  await page.mouse.down();
  await page.mouse.move(strip2.x + 40, strip2.y + strip2.height / 2 + 90, { steps: 6 });
  await page.mouse.up();
  const shorter = (await page.locator("#chat-input").boundingBox()).height;
  expect(shorter, "dragging down did not make the composer shorter").toBeLessThan(taller);

  // (e): nothing typed is lost, and the transcript was never scrolled.
  await expect(page.locator("#chat-input")).toHaveValue("a message I am still writing");
  expect(await page.evaluate(() => document.getElementById("chat-log").scrollTop)).toBe(0);

  // (c): the height is remembered for this surface.
  const [remembered, applied] = await page.evaluate(() => [
    localStorage.getItem("agentb.composer-height"),
    document.getElementById("chat-composer").style.getPropertyValue("--composer-height"),
  ]);
  expect(Number(remembered)).toBeGreaterThan(0);
  // What was remembered is the height that was applied; the rendered textarea is
  // that plus its own padding.
  expect(Number(remembered)).toBe(Number.parseFloat(applied));
  expect(shorter).toBeGreaterThanOrEqual(Number(remembered));
});

test("composer controls stay inside at every four pixels and live status stays visible", async ({ page }) => {
  const [tokensCSS, chatCSS] = await Promise.all([
    readFile(new URL("../../web/css/tokens.css", import.meta.url), "utf8"),
    readFile(new URL("../../web/css/chat.css", import.meta.url), "utf8"),
  ]);
  const body = `<!doctype html><html><head><style>${tokensCSS}${chatCSS}
    body{margin:0}.chat-page{height:600px}.chat-log{display:block}.chat-entry{height:52px}
  </style></head><body class="chat-page"><header class="app-shell"></header><div class="chat-budget"></div>
    <main id="chat-log" class="chat-log">${"<div class=\"chat-entry\">entry</div>".repeat(30)}<div id="chat-status-line" class="chat-live-status"><span>reading settings.js · 12s</span></div></main>
    <footer id="chat-composer" class="chat-composer"><div class="chat-resize-handle"></div><div class="chat-composer-row"><div class="chat-input-wrap"><div class="chat-pending-attachments"></div><textarea id="chat-input">${"word ".repeat(40)}</textarea><span class="chat-input-actions"><button class="composer-control">a</button><button class="composer-control">m</button><span class="chat-submit-actions"><button class="composer-control">s</button></span></span></div></div></footer>
    <script type="module">import { composerLayoutForHeight } from "/js/composer-resize.js";window.composerLayoutForHeight=composerLayoutForHeight;</script>
  </body></html>`;
  await page.route("**/*", (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/composer-fold.html") return route.fulfill({ contentType: "text/html", body });
    return route.fulfill({ path: webRoot + path.replace(/^\//, "") });
  });
  await page.goto("http://composer.test/composer-fold.html");
  await page.waitForFunction(() => typeof window.composerLayoutForHeight === "function");
  for (let height = 180; height >= 0; height -= 4) {
    const measured = await page.evaluate((value) => {
      const composer = document.getElementById("chat-composer");
      composer.style.setProperty("--composer-height", `${value}px`);
      composer.dataset.composerLayout = window.composerLayoutForHeight(value);
      const wrap = document.querySelector(".chat-input-wrap").getBoundingClientRect();
      const actions = document.querySelector(".chat-input-actions");
      return { layout:composer.dataset.composerLayout, wrap, display:getComputedStyle(actions).display, direction:getComputedStyle(actions).flexDirection, buttons:[...actions.querySelectorAll("button")].map((button) => button.getBoundingClientRect()) };
    }, height);
    if (measured.layout === "shut") expect(measured.display, `buttons at ${height}px`).toBe("none");
    else {
      expect(measured.direction, `fold at ${height}px`).toBe(measured.layout === "row" ? "row" : "column");
      for (const button of measured.buttons) {
        expect(button.left, `left at ${height}px`).toBeGreaterThanOrEqual(measured.wrap.left - .1);
        expect(button.right, `right at ${height}px`).toBeLessThanOrEqual(measured.wrap.right + .1);
        expect(button.top, `top at ${height}px`).toBeGreaterThanOrEqual(measured.wrap.top - .1);
        expect(button.bottom, `bottom at ${height}px`).toBeLessThanOrEqual(measured.wrap.bottom + .1);
      }
    }
  }
  const padding = await page.evaluate(() => {
    const composer = document.getElementById("chat-composer");
    composer.style.setProperty("--composer-height", "44px");
    composer.dataset.composerLayout = window.composerLayoutForHeight(44);
    return Number.parseFloat(getComputedStyle(document.getElementById("chat-input")).paddingRight);
  });
  expect(padding, "a long line can run beneath row controls").toBeGreaterThanOrEqual(90);
  const sticky = await page.evaluate(() => {
    const log = document.getElementById("chat-log");
    log.scrollTop = log.scrollHeight;
    log.scrollTop = Math.max(0, log.scrollTop - 2000);
    const outer = log.getBoundingClientRect(), status = document.getElementById("chat-status-line").getBoundingClientRect();
    return { outer, status, last:log.lastElementChild.id };
  });
  expect(sticky.last).toBe("chat-status-line");
  expect(sticky.status.top).toBeGreaterThanOrEqual(sticky.outer.top);
  expect(sticky.status.bottom).toBeLessThanOrEqual(sticky.outer.bottom);
});

test("every chat chip holds its text across the layout scale matrix", async ({ page }) => {
  const tokensCSS = await readFile(new URL("../../web/css/tokens.css", import.meta.url), "utf8");
  await mkdir("logs/evidence/rel-1.62.91/captures", { recursive:true });
  for (const { textSize, displayScale } of chatScaleMatrix) {
    await page.setViewportSize({ width: 1280, height: 1800 });
    await page.setContent(`<style>${tokensCSS}${chatCSS}body{margin:0}.chat-page{--chat-size:${textSize}px;display:block;height:auto;overflow:visible}.fixture{width:360px;padding:12px;transform:scale(${displayScale / 100});transform-origin:top left;display:grid;grid-template-columns:minmax(0,1fr);gap:calc(var(--ct)/2)}.file-chips{margin:0}</style><div class="chat-page"><main class="fixture">
      <button data-chip class="chat-work-summary">worked · 1m 24s</button>
      <div class="chat-narration-line"><button data-chip class="chat-step-summary">call_service · calendar</button></div>
      <button data-chip class="thinking-line"><span class="thought-glyph"><svg viewBox="0 0 11 9"></svg></span>thought for 12s</button>
      <div class="file-chips" data-wrap><span data-chip class="file-chip"><span class="file-chip-name">report-long-name.pdf</span><span class="file-chip-size">42 KB</span></span><span data-chip class="file-chip"><span class="file-chip-name">notes.txt</span></span></div>
      <button data-chip class="tool-tick"><span>read_file</span><span class="tool-key">a-long-file-name.txt</span><span>done</span></button>
      <div data-chip class="attachment-bubble chat-bubble"><span class="attachment-bubble-type">PDF</span><span class="attachment-bubble-words"><span class="attachment-bubble-name">manual.pdf</span><span class="attachment-bubble-state">ready</span></span><button class="attachment-bubble-remove">×</button></div>
      <div data-chip class="chat-live-status">reading settings.js · 12s</div>
      <div data-chip class="chat-content chat-bubble">make the text larger</div>
    </main></div>`);
    const result = await page.evaluate(() => {
      const root = document.querySelector(".fixture").getBoundingClientRect();
      const chips = [...document.querySelectorAll("[data-chip]")];
      const boxes = chips.map((node) => { const style=getComputedStyle(node); return { name:node.className, box:node.getBoundingClientRect(), whole:node.scrollWidth <= node.clientWidth + 1 && node.scrollHeight <= node.clientHeight + 1, size:[node.clientWidth,node.clientHeight,node.scrollWidth,node.scrollHeight], widths:[style.width,style.minWidth,style.maxWidth,style.whiteSpace] }; });
      const overlaps = [];
      for (let left = 0; left < boxes.length; left++) for (let right = left + 1; right < boxes.length; right++) {
        const a = boxes[left].box, b = boxes[right].box;
        if (Math.min(a.right,b.right)-Math.max(a.left,b.left)>1 && Math.min(a.bottom,b.bottom)-Math.max(a.top,b.top)>1 && !chips[left].contains(chips[right]) && !chips[right].contains(chips[left])) overlaps.push([boxes[left].name,boxes[right].name]);
      }
      const wrap = document.querySelector("[data-wrap]");
      return { root, boxes, overlaps, wrapWhole:wrap.scrollWidth <= wrap.clientWidth + 1 };
    });
    expect(result.boxes.filter(({ whole }) => !whole), `${textSize}px at ${displayScale}% clipped text`).toEqual([]);
    expect(result.overlaps, `${textSize}px at ${displayScale}% overlapped`).toEqual([]);
    expect(result.wrapWhole, `${textSize}px at ${displayScale}% did not wrap`).toBe(true);
    expect(result.boxes.filter(({ box }) => box.left < result.root.left - 2 || box.right > result.root.right + 2), `${textSize}px/${displayScale}% outside chat`).toEqual([]);
    if (displayScale === 100 || textSize === 12) await page.screenshot({ path:`logs/evidence/rel-1.62.91/captures/after-${textSize}px-${displayScale}.png`, animations:"disabled" });
  }
});

test("thinking and tool chips fit their words while open bodies keep the fold width 2v3", async ({ page }) => { await mkdir("logs/evidence/rel-1.62.116/captures", { recursive:true }); await page.setViewportSize({ width:1280, height:860 });
  const fixture = `<style>${tokensCSS}${chatCSS}body{margin:0;background:var(--well)}.chat-page{--chat-size:12px;display:block;width:100%;height:auto;padding:24px}.proof{display:grid;gap:8px;width:min(900px,100%)}.proof-fold{display:grid;min-width:0}.proof-body{min-height:40px}</style><main class="chat-page"><section class="proof"><div class="proof-fold"><button class="thinking-line"><span class="thought-glyph">◇</span>thinking · ~770 tokens · 26.2 s</button><div class="thinking-body proof-body" hidden>Reasoning stays the width of its fold.</div></div><div class="proof-fold"><button class="tool-tick"><span class="tool-name">remember</span><span class="tool-key">On long research/creative answers, wants concise conclusions</span><span class="tool-state">ok</span><span class="tool-ms">35 ms</span></button><div class="tool-detail proof-body" hidden>Tool detail stays the width of its fold.</div></div><div class="proof-fold"><button class="thinking-line"><span class="thought-glyph">◇</span>thinking · 94 tokens</button></div><div class="proof-fold"><button class="tool-tick long-tool"><span class="tool-name">read_file</span><span class="tool-key">a deliberately long target whose middle must be cut with an ellipsis before this chip could ever span the transcript width</span><span class="tool-state">ok</span><span class="tool-ms">8 ms</span></button></div><div class="proof-fold"><button class="chat-step-summary"><span class="chat-step-sentence">3 tool calls</span></button></div><div class="proof-fold"><button class="chat-tool-group-head">read 3 files</button></div></section></main>`;
  await page.setContent(fixture);
  const measured = await page.evaluate(() => { const box = (selector) => document.querySelector(selector).getBoundingClientRect(), tool = document.querySelector(".tool-tick"), key = tool.querySelector(".tool-key"), state = tool.querySelector(".tool-state"), longKey = document.querySelector(".long-tool .tool-key"); return { fold:box(".proof-fold"), thinking:box(".thinking-line"), tool:box(".tool-tick"), keyStateGap:box(".tool-state").left-box(".tool-key").right, longEllipsis:longKey.scrollWidth>longKey.clientWidth, heads:[box(".chat-step-summary").width,box(".chat-tool-group-head").width] }; });
  console.log(`2v3 fitted tool chip at 1280px: ${Math.round(measured.tool.width)}px`);
  expect(measured.thinking.width).toBeLessThan(measured.fold.width); expect(measured.tool.width).toBeLessThan(measured.fold.width); expect(measured.keyStateGap).toBeLessThanOrEqual(8); expect(measured.longEllipsis).toBe(true); expect(measured.heads.every((width) => width < measured.fold.width)).toBe(true);
  await page.screenshot({ path:"logs/evidence/rel-1.62.116/captures/chips-1280.png", animations:"disabled" }); await page.locator(".proof-body").evaluateAll((nodes) => nodes.forEach((node) => { node.hidden=false; })); const open = await page.evaluate(() => ({ body:document.querySelector(".tool-detail").getBoundingClientRect().width, chip:document.querySelector(".tool-tick").getBoundingClientRect().width })); expect(open.body).toBeGreaterThan(open.chip); await page.screenshot({ path:"logs/evidence/rel-1.62.116/captures/chips-open-1280.png", animations:"disabled" });
  await page.setViewportSize({ width:304, height:860 }); await page.setContent(fixture);
  const narrow = await page.locator(".tool-tick").first().evaluate((node) => ({ height:node.getBoundingClientRect().height, line:Number.parseFloat(getComputedStyle(node).lineHeight), whole:node.scrollWidth <= node.clientWidth + 1 }));
  expect(narrow.height).toBeGreaterThan(narrow.line); expect(narrow.whole).toBe(true); await page.locator(".long-tool").evaluate((node) => { node.style.display="none"; });
  await page.screenshot({ path:"logs/evidence/rel-1.62.116/captures/chips-304.png", animations:"disabled" });
});

test("shared raised look and composer states match the approved page", async ({ page }) => {
  const tokensCSS = await readFile(new URL("../../web/css/tokens.css", import.meta.url), "utf8");
  await mkdir("logs/evidence/rel-1.62.91/captures", { recursive:true });
  await page.setContent(`<style>${tokensCSS}${appCSS}${chatCSS}
    body{overflow:auto}.look-page{--ct:12px;--lead:20px;--lead-tight:16px;min-height:100vh;padding:24px;background:var(--well);font:12px/20px var(--sans)}.look-grid{display:grid;grid-template-columns:minmax(0,1fr) minmax(320px,42%);gap:24px}.look-stack{display:grid;align-content:start;gap:12px}.look-row{display:flex;align-items:flex-start;gap:10px;flex-wrap:wrap}.capture-menu{position:static}.capture-settings{position:static;transform:none;width:100%;height:320px;display:grid}.capture-composer{padding:0}.capture-composer .chat-input-wrap{height:112px}.capture-composer.short .chat-input-wrap{height:44px}.capture-composer .chat-input-actions{display:flex}.capture-composer.short .chat-input-actions{flex-direction:row}.capture-shut{height:4px}.capture-shut .chat-input-actions,.capture-shut textarea{display:none}.capture-shut .chat-resize-handle{display:block}.capture-table{border-collapse:collapse}.capture-table td{padding:4px 8px;border-bottom:1px solid var(--mute)}
  </style><main class="look-page"><div class="look-grid"><section class="look-stack">
    <div class="chat-content chat-bubble">make the settings window use the available space</div>
    <button class="chat-work-summary">worked · 1m 24s</button><div class="chat-narration-line"><button class="chat-step-summary">call_service · calendar</button></div><button class="thinking-line">thought for 12s</button>
    <div class="file-chips"><span class="file-chip"><span class="file-chip-name">report.pdf</span><span class="file-chip-size">42 KB</span></span></div>
    <div class="approval-card"><span>Approval needed</span><span>shell · outside folder</span><div class="approval-actions"><button>Allow once</button><button>Deny</button></div></div>
    <div class="chat-live-status">reading settings.js · 12s</div>
    <div class="tool-tick"><span>read_file</span><span>fixture.txt</span><span>done</span></div><table class="capture-table"><tbody><tr><td>field</td><td>value</td></tr></tbody></table><button id="plain-control">Plain button</button>
    <div class="shell-menu capture-menu"><button>Settings</button></div>
  </section><section class="look-stack"><div class="settings-page capture-settings"><header class="settings-head">Settings</header><div class="settings-content">Connections</div></div>
    <div class="chat-composer capture-composer"><div class="chat-input-wrap"><textarea>tall message box</textarea><span class="chat-input-actions"><button>a</button><button>m</button><button>s</button></span></div></div>
    <div class="chat-composer capture-composer short"><div class="chat-input-wrap"><textarea>short message box</textarea><span class="chat-input-actions"><button>a</button><button>m</button><button>s</button></span></div></div>
    <div class="capture-shut"><div class="chat-resize-handle"></div></div>
  </section></div></main>`);
  const measured = await page.evaluate(() => {
    const style = (selector) => { const value=getComputedStyle(document.querySelector(selector)); return [value.backgroundColor,value.borderTopColor,value.borderTopWidth,value.borderRadius,value.boxShadow]; };
    return { bubble:style(".chat-bubble"), chip:style(".file-chip"), menu:style(".shell-menu"), card:style(".approval-card"), settings:style(".settings-page"), composer:style(".chat-input-wrap"), status:style(".chat-live-status"), tool:style(".tool-tick"), table:style("table"), button:style("#plain-control") };
  });
  for (const name of ["chip","menu","card","settings","composer","status"]) expect(measured[name], name).toEqual(measured.bubble);
  for (const name of ["tool","table","button"]) expect(measured[name], `${name} acquired the raised look`).not.toEqual(measured.bubble);
  for (const viewport of [{width:1280,height:860},{width:1920,height:1080}]) { await page.setViewportSize(viewport); await page.screenshot({ path:`logs/evidence/rel-1.62.91/captures/raised-composer-${viewport.width}.png`, animations:"disabled" }); }
});

// Item 2ps / I12: retained history must not make the first usable browser frame
// grow with what is stored. This runs the real document and modules in headless
// Edge; only the server-authored event stream is replaced with a deterministic
// snapshot so the two retained-size arms differ in no other way.
test("first usable frame does not grow with retained chat history", async ({ browser }) => {
  test.setTimeout(180_000);
  const fixture = (chats, entries) => {
    const sessions = {};
    for (let chat = 0; chat < chats; chat++) {
      const id = `chat-${chat}`;
      const retained = chat === 0 ? entries : 0;
      const visible = Math.min(50, retained);
      const start = retained - visible;
      const timeline = Array.from({ length: visible }, (_, index) => ({ type: "tool.result", session_id: id, run_id: "run", data: { call_id: start + index, result: "fixture" } }));
      const transcript = Array.from({ length: visible }, (_, index) => ({ type: (start + index) % 2 ? "agent" : "user", key: `entry:${start + index}`, text: "fixture", done: true }));
      sessions[id] = { schema_version: 1, cursor: { generation: `${id}.jsonl`, offset: entries + 1 }, complete: true, id, label: id,
        role: "b", created_at: "2026-10-01T00:00:00Z", run: { status: "idle" }, tools: [], messages: [], budget: {}, activity: { completed_stages: [] },
        timeline, chat: transcript, history_start: start, history_end: retained, history_total: retained, runnable: true, closed: false };
    }
    return { sessions, connections: [], config: { agents: [{ name: "agent_b", b: "fixture" }], connections: [] }, flow: { stages: [], edges: [] }, tools: [], plans: [], profiles: { active: "", names: [] }, build: {} };
  };
  const measure = async (state) => {
    const context = await browser.newContext({ viewport: { width: 1250, height: 975 } });
    await context.addInitScript(({ snapshot }) => {
      sessionStorage.setItem("agentb.selection", JSON.stringify({ agent_id: "agent_b", session_id: "chat-0", surface: { kind: "chat", key: "chat-0" } }));
      class FixtureEvents {
        constructor() {
          this.listeners = new Map();
          setTimeout(() => {
            this.onopen?.();
            const event = { data: JSON.stringify({ type: "snapshot", data: snapshot }) };
            for (const listener of this.listeners.get("snapshot") || []) listener(event);
          });
        }
        addEventListener(type, listener) { this.listeners.set(type, [...(this.listeners.get(type) || []), listener]); }
        close() {}
      }
      globalThis.EventSource = FixtureEvents;
    }, { snapshot: state });
    const page = await context.newPage();
    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/chat") return route.fulfill({ contentType: "text/html", body: indexHTML });
      if (url.pathname.startsWith("/api/")) return route.fulfill({ contentType: "application/json", body: "{}" });
      const relative = url.pathname.replace(/^\/static\//, "");
      return route.fulfill({ path: webRoot + relative });
    });
    await page.goto("http://first-frame.test/chat?setup=skip&session=chat-0", { waitUntil: "domcontentloaded" });
    await page.waitForFunction(() => {
      const input = document.querySelector("#chat-task");
      return input && !input.disabled;
    });
    const elapsed = await page.evaluate(() => performance.now());
    await context.close();
    return elapsed;
  };
  const median = (values) => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)];
  const smallState = fixture(1, 0);
  const largeState = fixture(30, 3000);
  const small = [], large = [];
  for (let index = 0; index < 5; index++) {
    small.push(await measure(smallState));
    console.log(`2ps empty sample ${index + 1}=${small.at(-1).toFixed(1)}ms`);
    large.push(await measure(largeState));
    console.log(`2ps large sample ${index + 1}=${large.at(-1).toFixed(1)}ms`);
  }
  const smallMedian = median(small), largeMedian = median(large);
  console.log(`2ps first usable frame empty=${smallMedian.toFixed(1)}ms large=${largeMedian.toFixed(1)}ms ratio=${(largeMedian / smallMedian).toFixed(2)}x`);
  expect(largeMedian, `empty=${smallMedian.toFixed(1)}ms large=${largeMedian.toFixed(1)}ms`).toBeLessThanOrEqual(2 * smallMedian);
});

test("a restored long chat keeps its words and scroll position 2t3", async ({ browser }) => {
  const id = "chat-restored-2t3", notice = "Agent changed from fixture-one to fixture-two.";
  const chat = Array.from({ length: 120 }, (_, index) => ({ type: "user", key: `message:${index}`, text: `retained line ${index} ${"content ".repeat(8)}` }));
  chat.splice(20, 0, { type: "notice", key: "event:words-top", text: notice });
  chat.push({ type: "user", key: "message:steps", text: "Inspect the retained steps." },
    { type: "tool", key: "tool:one", name: "read_file", args: { path: "C:\\synthetic\\one.txt" }, result: { ok: true, ms: 1 }, content: "one" },
    { type: "notice", key: "event:words-group", text: notice },
    { type: "tool", key: "tool:two", name: "read_file", args: { path: "C:\\synthetic\\two.txt" }, result: { ok: true, ms: 1 }, content: "two" },
    { type: "user", key: "message:malformed", text: "Inspect a malformed record." }, { type: "notice", key: "event:malformed" });
  const session = { schema_version: 1, cursor: { generation: `${id}.jsonl`, offset: 300 }, complete: true, id, label: "Restored fixture",
    agent_id: "agent_b", role: "b", created_at: "2026-10-09T00:00:00Z", run: { status: "idle" }, tools: [], messages: [], budget: {},
    activity: { completed_stages: [] }, timeline: [], chat, history_start: 100, history_end: 224, history_total: 224, runnable: true, closed: false };
  const snapshot = { sessions: { [id]: session }, connections: [], config: { agents: [{ name: "agent_b", b: "fixture" }], connections: [] },
    flow: { stages: [], edges: [] }, tools: [], plans: [], profiles: { active: "", names: [] }, build: {} };
  const context = await browser.newContext({ viewport: { width: 900, height: 520 } });
  await context.addInitScript(({ snapshot, id }) => {
    sessionStorage.setItem("agentb.selection", JSON.stringify({ agent_id: "agent_b", session_id: id, surface: { kind: "chat", key: id } }));
    class FixtureEvents { constructor() { this.listeners = new Map(); globalThis.restoredEvents = this; setTimeout(() => this.emit("snapshot", { type: "snapshot", data: snapshot })); }
      addEventListener(type, listener) { this.listeners.set(type, [...(this.listeners.get(type) || []), listener]); }
      emit(type, value) { for (const listener of this.listeners.get(type) || []) listener({ data: JSON.stringify(value) }); } close() {} }
    globalThis.EventSource = FixtureEvents;
  }, { snapshot, id });
  const page = await context.newPage();
  await page.route("**/*", async (route) => { const url = new URL(route.request().url());
    if (url.pathname === "/chat") return route.fulfill({ contentType: "text/html", body: indexHTML });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ contentType: "application/json", body: "{}" });
    return route.fulfill({ path: webRoot + url.pathname.replace(/^\/static\//, "") }); });
  await page.goto(`http://restored-2t3.test/chat?setup=skip&session=${id}`, { waitUntil: "domcontentloaded" });
  const log = page.locator("#chat-log"); await expect(log).toContainText("retained line 119");
  await page.locator(".chat-list-menu-button").click(); await page.locator(".chat-list-main-menu").getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByTitle("Close").click();
  const measurements = await log.evaluate((node) => ({ scrollHeight: node.scrollHeight, clientHeight: node.clientHeight, start: node.scrollTop }));
  await log.hover(); await page.mouse.wheel(0, -500); await page.waitForTimeout(50);
  measurements.wheel = await log.evaluate((node) => node.scrollTop);
  await log.evaluate((node) => { node.scrollTop = Math.floor(node.scrollHeight / 2); node.dispatchEvent(new Event("scroll")); });
  measurements.scrollbar = await log.evaluate((node) => node.scrollTop);
  await log.focus(); await page.keyboard.press("PageUp"); await page.waitForTimeout(150); measurements.keyUp = await log.evaluate((node) => node.scrollTop);
  await page.keyboard.press("PageDown"); await page.waitForTimeout(150); measurements.keyDown = await log.evaluate((node) => node.scrollTop);
  const held = measurements.scrollbar;
  await page.evaluate(({ snapshot }) => { for (let pass = 0; pass < 10; pass++) globalThis.restoredEvents.emit("snapshot", { type: "snapshot", data: structuredClone(snapshot) }); }, { snapshot });
  await page.waitForTimeout(100); measurements.afterRenders = await log.evaluate((node) => node.scrollTop);
  console.log("2t3 restored scroll", JSON.stringify(measurements));
  expect(measurements.scrollHeight).toBeGreaterThan(measurements.clientHeight);
  expect(measurements.wheel).toBeLessThan(measurements.start); expect(measurements.keyUp).toBeLessThan(measurements.scrollbar); expect(measurements.keyDown).toBeGreaterThan(measurements.keyUp);
  expect(Math.abs(measurements.afterRenders - held)).toBeLessThanOrEqual(1);
  const fold = page.locator(".chat-step-summary").filter({ hasText: "Read 2 files" }); await fold.click();
  await page.locator(".chat-step-summary").filter({ hasText: "1 row" }).click();
  await expect(page.getByText(notice, { exact: true })).toHaveCount(2);
  await expect(page.locator(".chat-notice-row").getByText(notice, { exact: true })).toHaveCount(1);
  await expect(page.locator(".chat-step-fold:not(.headerless)").getByText(notice, { exact: true })).toHaveCount(1);
  await expect(page.locator(".chat-render-failure")).toHaveText("notice event:malformed could not render · notice event is missing or is not an object");
  const copied = await log.evaluate((node) => { const selection = getSelection(), range = document.createRange(), data = new DataTransfer(); range.selectNodeContents(node); selection.removeAllRanges(); selection.addRange(range); document.dispatchEvent(new ClipboardEvent("copy", { clipboardData: data, bubbles: true, cancelable: true })); selection.removeAllRanges(); return data.getData("text/plain"); });
  expect(copied.match(new RegExp(notice.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "g"))).toHaveLength(2);
  await page.getByText(notice, { exact: true }).last().scrollIntoViewIfNeeded();
  await page.screenshot({ path: "test-results/2t3-agent-change-notice.png" });
  await context.close();
});

test("fresh context leaves every transcript entry visible and says how much and why", async ({ browser }) => {
  const id = "chat-fresh";
  const snapshot = {
    sessions: { [id]: { schema_version: 1, cursor: { generation: `${id}.jsonl`, offset: 4 }, complete: true, id, label: "long chat",
      role: "b", created_at: "2026-10-05T00:00:00Z", run: { status: "idle" }, tools: [], messages: [], budget: {}, activity: { completed_stages: [] },
      timeline: [], chat: [
        { type: "user", key: "message:u1", text: "FIRST VISIBLE TURN" },
        { type: "agent", key: "turn:r1:1", text: "SECOND VISIBLE TURN", done: true },
        { type: "notice", key: "event:3", run_id: "r1", event: { seq: 3, type: "compaction", session_id: id, run_id: "r1", data: { kind: "fresh", trigger: "summary_pct", before: 1000, after: 100 } } },
        ...Array.from({length:18},(_,index)=>({type:index%2?"agent":"user",key:`space-a:${index}`,text:`visible retained row ${index} ${"work ".repeat(12)}`,done:true})),
        { type: "notice", key: "event:22", run_id: "r2", event: { seq: 22, type: "compaction", session_id: id, run_id: "r2", data: { kind: "fresh", trigger: "summary_pct", fallback_reason: "error", before: 1100, after: 120 } } },
        ...Array.from({length:18},(_,index)=>({type:index%2?"agent":"user",key:`space-b:${index}`,text:`visible later row ${index} ${"work ".repeat(12)}`,done:true})),
        { type: "notice", key: "event:41", run_id: "r3", event: { seq: 41, type: "run.stopped", session_id: id, run_id: "r3", data: { reason: "context_exhausted", detail: "newest result is 17000 tokens and does not fit the 16000-token window" } } },
      ], runnable: true, closed: false } },
    connections: [], config: { agents: [{ name: "agent_b", b: "fixture" }], connections: [] }, flow: { stages: [], edges: [] }, tools: [], plans: [], profiles: { active: "", names: [] }, build: {},
  };
  for (const width of [304, 1280]) {
    const context = await browser.newContext({ viewport: { width, height: width === 1280 ? 860 : 700 } });
    await context.addInitScript(({ snapshot, id }) => {
      sessionStorage.setItem("agentb.selection", JSON.stringify({ agent_id: "agent_b", session_id: id, surface: { kind: "chat", key: id } }));
      localStorage.setItem("agentb.chat-list", JSON.stringify({ width: 240, hidden: true }));
      class FixtureEvents {
        constructor() { this.listeners = new Map(); setTimeout(() => { this.onopen?.(); this.emit("snapshot", { type: "snapshot", data: snapshot }); }); }
        addEventListener(type, listener) { this.listeners.set(type, [...(this.listeners.get(type) || []), listener]); }
        emit(type, value) { for (const listener of this.listeners.get(type) || []) listener({ data: JSON.stringify(value) }); }
        close() {}
      }
      globalThis.EventSource = FixtureEvents;
    }, { snapshot, id });
    const page = await context.newPage();
    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/chat") return route.fulfill({ contentType: "text/html", body: indexHTML });
      if (url.pathname.startsWith("/api/")) return route.fulfill({ contentType: "application/json", body: "{}" });
      return route.fulfill({ path: webRoot + url.pathname.replace(/^\/static\//, "") });
    });
    await page.goto(`http://fresh-context.test/chat?setup=skip&session=${id}`, { waitUntil: "domcontentloaded" });
    const log = page.locator("#chat-log");
    await expect(log).toContainText("FIRST VISIBLE TURN");
    await expect(log).toContainText("SECOND VISIBLE TURN");
    const line = "fresh context · 1000 → 100 tokens · window full · earlier turns searchable";
    const fallback = "Earlier work was put aside without a summary because of error; the record can be searched with chat_history.";
    const stopped = "stopped: context exhausted · newest result is 17000 tokens and does not fit the 16000-token window";
    await expect(log).toContainText(line);
    await expect(log).toContainText(fallback);
    for (const fold of await page.locator(".chat-step-summary:visible").all()) {
      if (await fold.getAttribute("aria-expanded") !== "true") await fold.click();
    }
    await expect(log).toContainText(stopped);
    await expect(log).not.toContainText("Fresh-context hand-off:");
    const notice = log.getByText(line, { exact: true });
    await expect(notice).toBeVisible();
    expect(await notice.evaluate((node) => node.scrollWidth <= node.clientWidth)).toBe(true);
    await page.screenshot({ path: `test-results/2sz-fresh-${width}.png`, fullPage: true });
    if (width === 1280) {
      await mkdir("logs/evidence/rel-1.62.103/captures", {recursive:true});
      for (const [name,text] of [["compaction-line",line],["compaction-fallback",fallback],["compaction-too-big",stopped]]) {
        await log.getByText(text,{exact:true}).scrollIntoViewIfNeeded();
        await page.screenshot({path:`logs/evidence/rel-1.62.103/captures/${name}-1280x860.png`,animations:"disabled"});
      }
    }
    await context.close();
  }
});

test("2ql finished and live s56-shaped replays stay compact and keep every entry reachable", async ({ browser }) => {
  test.setTimeout(60_000);
  const auditDisclosures = async (page) => page.evaluate(async () => {
    const failures = [];
    for (;;) {
      const button = [...document.querySelectorAll(".chat-response button[aria-expanded]:not([data-audited])")].find((node) => !node.hidden && node.getClientRects().length);
      if (!button) break;
      button.dataset.audited = "1";
      const before = button.getAttribute("aria-expanded");
      button.click(); await new Promise(requestAnimationFrame);
      if (button.getAttribute("aria-expanded") === before) failures.push(button.className || button.textContent);
      button.click(); await new Promise(requestAnimationFrame);
      if (before === "false") { button.click(); await new Promise(requestAnimationFrame); }
    }
    return failures;
  });
  const id = "chat-2ql";
  const runID = "run-2ql";
  const replay = [
    { type: "user", key: "user-2ql", text: "Review the synthetic build record." },
    { type: "agent", key: "narration-one", run_id: runID, text: "Checked the build inputs.", reasoning: "Compared the recorded inputs.", reasoningTokens: 7, done: true, thinkingMS: 40 },
    { type: "tool", key: "failed-then-retried", run_id: runID, name: "shell", args: { command: "git stash pop --index synthetic-extra" }, result: { ok: false, error_class: "exit_nonzero", ms: 12 }, content: "synthetic conflict" },
    { type: "tool", key: "retry-succeeded", run_id: runID, name: "shell", args: { command: "git stash pop --index synthetic-extra" }, result: { ok: true, ms: 18 }, content: "synthetic success" },
    { type: "agent", key: "narration-two", run_id: runID, text: "Verified the rebuilt output.", reasoning: "Read the synthetic verification result.", reasoningTokens: 11, done: true, thinkingMS: 55 },
    { type: "tool", key: "final-tool", run_id: runID, name: "read_file", args: { path: "C:\\synthetic\\evidence\\result.txt" }, result: { ok: true, ms: 8 }, content: "synthetic proof" },
    { type: "tool", key: "edit-first", run_id: runID, name: "edit_file", args: { path: "C:\\synthetic\\first.txt" }, result: { ok: true, ms: 4 }, content: "changed" },
    { type: "tool", key: "write-second", run_id: runID, name: "write_file", args: { path: "C:\\synthetic\\second.txt" }, result: { ok: true, ms: 5 }, content: "changed" },
    { type: "tool", key: "write-third", run_id: runID, name: "write_file", args: { path: "C:\\synthetic\\third.txt" }, result: { ok: true, ms: 6 }, content: "created" },
    { type: "tool", key: "todo-current", run_id: runID, name: "todo", args: { items: [{ text: "Inspect inputs", status: "done" }, { text: "Run checks", status: "doing" }, { text: "Write report", status: "pending" }] }, result: { ok: true, ms: 1 }, content: "task list updated" },
    { type: "agent", key: "final-answer", run_id: runID, text: "The synthetic release proof is complete.", done: true },
  ];
  const snapshot = (status) => {
    const chat = status === "running" ? replay.slice(0, -1) : replay;
    return ({
    sessions: { [id]: { schema_version: 1, cursor: { generation: `${id}.jsonl`, offset: chat.length }, complete: true, id, label: "Synthetic replay",
      agent_id: "agent_b", role: "b", created_at: "2026-10-05T00:00:00Z", run: { status, run_id: runID }, tools: [], messages: [], budget: {},
      activity: status === "running" ? { completed_stages: [], started_at: Date.now(), stage: "execute", stage_state: "enter", active_tool: "read_file", tool_target: "result.txt" } : { completed_stages: [] }, timeline: [], chat, runnable: true, closed: false } },
    connections: [], config: { agents: [{ name: "agent_b", b: "fixture" }], connections: [] }, flow: { stages: [], edges: [] }, tools: [], plans: [], profiles: { active: "", names: [] }, build: {},
  }); };
  const open = async (status, host, initial = snapshot(status), expected = status === "running" ? "Verified the rebuilt output" : "synthetic release proof") => {
    const context = await browser.newContext({ viewport: { width: 1000, height: 760 } });
    await context.addInitScript(({ state, id }) => {
      sessionStorage.setItem("agentb.selection", JSON.stringify({ agent_id: "agent_b", session_id: id, surface: { kind: "chat", key: id } }));
      class FixtureEvents {
        constructor() { this.listeners = new Map(); globalThis.__fixtureEvents = this; setTimeout(() => this.emit("snapshot", { type: "snapshot", data: state })); }
        addEventListener(type, listener) { this.listeners.set(type, [...(this.listeners.get(type) || []), listener]); }
        emit(type, value) { for (const listener of this.listeners.get(type) || []) listener({ data: JSON.stringify(value) }); }
        close() {}
      }
      globalThis.EventSource = FixtureEvents;
    }, { state: initial, id });
    const page = await context.newPage();
    let heldFiles = [{ id:"edited", path:"first.txt", kind:"changed", changed_since:false }, { id:"created", path:"second.txt", kind:"created", changed_since:false }, { id:"removed", path:"third.txt", kind:"removed", changed_since:true }];
    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/chat") return route.fulfill({ contentType: "text/html", body: indexHTML });
      if (url.pathname === "/api/file-undo" && route.request().method() === "GET") {
        const file = heldFiles.find((item) => item.id === url.searchParams.get("file_id"));
        if (!file) return route.fulfill({ contentType:"application/json", body:JSON.stringify({ files:heldFiles }) });
        if (file.id === "created") return route.fulfill({ contentType:"application/json", body:JSON.stringify({ file, lines:Array.from({length:2000},(_,index)=>({kind:"added",new_line:index+1,text:`${index+1} ${"x".repeat(245)}`})), more:3000 }) });
        const long = "a long synthetic line ".repeat(24);
        return route.fulfill({ contentType:"application/json", body:JSON.stringify({ file, lines:[{kind:"context",old_line:1,new_line:1,text:"one"},{kind:"context",old_line:2,new_line:2,text:"two"},{kind:"context",old_line:3,new_line:3,text:"three"},{kind:"removed",old_line:4,text:"four"},{kind:"added",new_line:4,text:long},{kind:"context",old_line:5,new_line:5,text:"five"},{kind:"context",old_line:6,new_line:6,text:"six"},{kind:"context",old_line:7,new_line:7,text:"seven"}] }) });
      }
      if (url.pathname === "/api/file-undo" && route.request().method() === "POST") { const body = route.request().postDataJSON(); const changed = body.file_id === "removed"; if (body.file_id && !changed) heldFiles = heldFiles.filter((item) => item.id !== body.file_id); return route.fulfill({ contentType:"application/json", body:JSON.stringify(changed ? { restored:[], skipped:["third.txt"] } : { restored:["first.txt"], skipped:[] }) }); }
      if (url.pathname.startsWith("/api/")) return route.fulfill({ contentType: "application/json", body: "{}" });
      return route.fulfill({ path: webRoot + url.pathname.replace(/^\/static\//, "") });
    });
    await page.goto(`http://${host}/chat?setup=skip&session=${id}`, { waitUntil: "domcontentloaded" });
    await expect(page.locator("#chat-log")).toContainText(expected);
    return { context, page };
  };

  const compactStarted = Date.now() - 2100;
  const compact = snapshot("running"); compact.sessions[id].activity = { completed_stages: [], started_at: compactStarted, state_started_at: compactStarted, stage: "compact", stage_state: "enter" };
  const compacting = await open("running", "compact-2uv.test", compact); const compactStatus = compacting.page.locator("#chat-status-line");
  await expect.poll(async () => Number((await compactStatus.textContent()).match(/ · (\d+)s/)?.[1])).toBeGreaterThanOrEqual(2);
  expect(Number((await compactStatus.textContent()).match(/ · (\d+)s/)?.[1])).toBeLessThanOrEqual(Math.floor((Date.now() - compactStarted) / 1000)); await expect(compactStatus.locator(".agent-robot")).toHaveAttribute("data-doing", "compacting");
  const beforeTick = Number((await compactStatus.textContent()).match(/ · (\d+)s/)?.[1]);
  await compacting.page.waitForTimeout(1100); await expect.poll(async () => Number((await compactStatus.textContent()).match(/ · (\d+)s/)?.[1])).toBeGreaterThan(beforeTick); await mkdir("logs/evidence/rel-1.62.110/captures", { recursive: true });
  for (const width of [1280, 304]) { await compacting.page.setViewportSize({ width, height: 860 }); await compacting.page.evaluate((show) => { const panel = document.querySelector(".chat-list-panel"), handle = document.querySelector(".chat-list-handle"); document.body.classList.toggle("chat-list-visible", show); document.body.classList.toggle("chat-list-hidden", !show); panel.hidden = !show; handle.hidden = show; }, width > 360); expect(await compactStatus.evaluate((node) => ({ text: node.scrollWidth <= node.clientWidth, inside: node.getBoundingClientRect().right <= innerWidth }))).toEqual({ text: true, inside: true }); await compacting.page.screenshot({ path: `logs/evidence/rel-1.62.110/captures/compacting-${width}.png`, fullPage: true }); }
  await compacting.page.emulateMedia({ reducedMotion: "reduce" }); await expect(compactStatus.locator(".agent-robot-press")).toHaveCSS("display", "none");
  compact.sessions[id].activity = { completed_stages: [], started_at: Date.now(), state_started_at: Date.now(), stage: "call_model", stage_state: "enter", stream: {} }; await compacting.page.evaluate((next) => globalThis.__fixtureEvents.emit("snapshot", { type: "snapshot", data: next }), compact); await expect(compactStatus).toContainText("connecting · 0s"); console.log("2uv compacting page scenario PASS"); await compacting.context.close();

  const finished = await open("idle", "finished-2ql.test");
  const worked = finished.page.locator(".chat-work-summary");
  await expect(worked).toHaveCount(1);
  await expect(worked).toContainText(/Worked .* · 2 steps .*▸/);
  await expect(finished.page.getByText("The synthetic release proof is complete.")).toBeVisible();
  await expect(finished.page.getByText("Checked the build inputs.")).toHaveCount(0);
  await expect(finished.page.locator("#chat-status-line")).toHaveCount(0);
  await expect(finished.page.locator(".chat-work-summary")).toContainText("3 files · file tools only");
  await expect(finished.page.getByRole("button", { name: "Undo file changes" })).toHaveCount(1);
  const restingSummary = await finished.page.locator(".chat-work-summary").textContent();
  expect(restingSummary).toMatch(/Worked .* · 2 steps · 3 files · file tools only ▸/);
  await mkdir("logs/evidence/rel-1.62.36/captures", { recursive:true }); await mkdir("logs/evidence/rel-1.62.58/captures", { recursive:true });
  await finished.page.setViewportSize({ width:1280, height:860 });
  await finished.page.screenshot({ path:"logs/evidence/rel-1.62.36/captures/undo-file-changes-1280x860.png", fullPage:true });
  await finished.page.screenshot({ path:"logs/evidence/rel-1.62.58/captures/resting-summary-1280x860.png", fullPage:true });
  await finished.page.setViewportSize({ width:1000, height:760 });
  await finished.page.getByRole("button", { name: "3 files changed" }).click();
  await expect(finished.page.locator(".chat-file-open")).toHaveText(["first.txt · changed", "second.txt · created", "third.txt · removed"]);
  expect(await finished.page.locator(".chat-work-summary").textContent()).toBe(restingSummary);
  await finished.page.locator(".chat-file-open").first().click();
  await expect(finished.page.locator(".chat-file-diff-line")).toHaveCount(8);
  await expect(finished.page.locator(".chat-file-diff-line.removed")).toContainText("four");
  await expect(finished.page.locator(".chat-file-diff-line.added")).toContainText("a long synthetic line");
  for (const width of [1280, 304]) { await finished.page.setViewportSize({ width, height:860 }); await finished.page.evaluate((show) => { const panel=document.querySelector(".chat-list-panel"), handle=document.querySelector(".chat-list-handle"); document.body.classList.toggle("chat-list-visible",show); document.body.classList.toggle("chat-list-hidden",!show); panel.hidden=!show; handle.hidden=show; }, width > 360); await finished.page.screenshot({ path:`logs/evidence/rel-1.62.58/captures/file-diff-${width}.png`, fullPage:true }); }
  await finished.page.setViewportSize({ width:1000, height:760 });
  await finished.page.evaluate(() => { const panel=document.querySelector(".chat-list-panel"), handle=document.querySelector(".chat-list-handle"); document.body.classList.add("chat-list-visible"); document.body.classList.remove("chat-list-hidden"); panel.hidden=false; handle.hidden=true; });
  finished.page.once("dialog", (dialog) => dialog.accept());
  await finished.page.locator(".chat-file-undo").first().click();
  await expect(finished.page.getByRole("button", { name: "2 files changed" })).toBeVisible();
  await expect(finished.page.locator(".chat-file-open")).toHaveCount(2);
  const drawMS = await finished.page.evaluate(async () => { const start=performance.now(); [...document.querySelectorAll(".chat-file-open")].find((node)=>node.textContent.includes("second.txt")).click(); for (let index=0; index<120 && document.querySelectorAll(".chat-file-diff-line").length!==2000; index++) await new Promise(requestAnimationFrame); return performance.now()-start; });
  console.log("2sk 512KiB 2000-line draw", `${drawMS.toFixed(3)}ms`);
  expect(drawMS).toBeLessThan(300);
  await expect(finished.page.locator(".chat-file-diff-more")).toHaveText("3,000 more");
  await finished.page.locator(".chat-file-open").filter({ hasText:"second.txt" }).click();
  await finished.page.locator(".chat-file-open").filter({ hasText:"third.txt" }).click();
  await expect(finished.page.locator(".chat-file-changed")).toHaveText("Changed again since this run; Undo this file will leave it unchanged.");
  finished.page.once("dialog", (dialog) => dialog.accept());
  await finished.page.locator(".chat-file-item").filter({ hasText:"third.txt" }).locator(".chat-file-undo").click();
  await expect(finished.page.locator(".chat-work-undo-result")).toContainText("left changed third.txt");
  await expect(finished.page.getByRole("button", { name: "2 files changed" })).toBeVisible();
  await finished.page.getByRole("button", { name: "2 files changed" }).click();
  await expect(finished.page.locator(".chat-file-list")).toHaveCount(0);
  await finished.page.setViewportSize({ width:1000, height:760 });
  await expect(finished.page.locator(".chat-todo")).toHaveCount(1);
  await expect(finished.page.locator(".chat-todo-row")).toHaveText(["✓ Inspect inputs", "→ Run checks", "· Write report"]);
  await mkdir("logs/evidence/rel-1.62.35/captures", { recursive: true });
  await finished.page.screenshot({ path: "logs/evidence/rel-1.62.35/captures/todo-1280x860.png", fullPage: true });
  await finished.page.screenshot({ path: "test-results/2ql-s56-finished.png", fullPage: true });
  await finished.page.screenshot({ path: "test-results/2qm-idle.png", fullPage: true });
  await finished.page.locator(".chat-work-summary-toggle").click();
  await expect(finished.page.getByText("Checked the build inputs.")).toBeVisible();
  await expect(finished.page.locator(".chat-narration-line .chat-step-summary")).toHaveCount(2);
  await expect(finished.page.locator(".chat-step-fold.alarm")).toHaveCount(0);
  await expect(finished.page.locator(".chat-step-summary").filter({ hasText: "ran 2 commands" })).toContainText("Thought for a moment");
  await expect(finished.page.getByText("thought · 7 tokens")).toBeVisible();
  await expect(finished.page.locator(".tool-tick").first()).toContainText(/shell\s*·\s*git stash pop/);
  expect(await auditDisclosures(finished.page)).toEqual([]);
  await finished.page.evaluate(() => document.querySelector(".chat-response").insertAdjacentHTML("beforeend", '<button aria-expanded="false">ignored disclosure</button>'));
  expect(await auditDisclosures(finished.page)).toEqual(["ignored disclosure"]);
  await finished.page.evaluate(() => { [...document.querySelectorAll("button")].find((button) => button.textContent === "ignored disclosure")?.remove(); for (const button of document.querySelectorAll('.tool-tick[aria-expanded="true"],.thinking-line[aria-expanded="true"]')) button.click(); document.querySelector("#chat-log").insertAdjacentHTML("beforeend", '<section class="chat-entry chat-summary" aria-label="summary"><div class="chat-content">summary row</div></section><section class="chat-entry chat-notice-row" aria-label="agent_b"><div class="chat-content">harness note</div></section>'); document.querySelector("#chat-log").scrollTop = 0; });
	await mkdir("logs/evidence/rel-1.62.76/captures", { recursive: true }); for (const width of [1280, 640]) { await finished.page.setViewportSize({ width, height: 860 }); await finished.page.evaluate(() => { const style = document.createElement("style"); style.id = "old-speaker-rail"; style.textContent = ".chat-entry:not(.chat-user){grid-template-columns:30px minmax(0,1fr);gap:8px}.chat-speaker{display:flex;flex-direction:column;align-items:center;color:var(--mute);font-size:8px}.chat-speaker img{width:20px;height:20px}.chat-notice-row .chat-content{grid-column:2}"; document.head.append(style); for (const row of document.querySelectorAll("#chat-log > .chat-entry:not(.chat-user)")) { const speaker = document.createElement("div"); speaker.className = "chat-speaker"; if (row.classList.contains("chat-agent")) speaker.innerHTML = '<img src="/static/assets/agent.svg" alt=""><span>agent_b</span>'; row.prepend(speaker); } }); await finished.page.screenshot({ path: `logs/evidence/rel-1.62.76/captures/2tk-before-${width}.png`, fullPage: true }); await finished.page.evaluate(() => { document.querySelector("#old-speaker-rail")?.remove(); for (const node of document.querySelectorAll("#chat-log > .chat-entry > .chat-speaker")) node.remove(); }); await finished.page.screenshot({ path: `logs/evidence/rel-1.62.76/captures/2tk-after-${width}.png`, fullPage: true }); }
  await finished.page.setViewportSize({ width: 1280, height: 900 });
	const workerState = snapshot("idle"); workerState.sessions[id].chat = [{ type: "agent", key: "worker-answer", agent_role: "c", text: "Worker answer in the plan thread.", done: true }]; workerState.sessions[id].cursor.offset = 1;
	const workerPage = await open("idle", "worker-2tk.test", workerState, "Worker answer"); await expect(workerPage.page.locator(".chat-response-author")).toHaveText("agent_c"); await expect(workerPage.page.locator(".chat-response")).toHaveAttribute("aria-label", "agent_c"); await workerPage.context.close();
  const rowLayout = async (pixels) => finished.page.evaluate((value) => {
    document.querySelector(".chat-page").style.setProperty("--chat-size", `${value}px`);
    document.documentElement.dataset.chatListWrap = String(value >= 28);
    const log = document.querySelector("#chat-log").getBoundingClientRect(), rows = [...document.querySelectorAll("#chat-log > .chat-entry:not(.chat-user)")];
    const width = (node) => Math.round(node.getBoundingClientRect().width);
    const proseRows = [...document.querySelectorAll(".chat-response-content")].filter((node) => node.checkVisibility()).map(width);
    const foldHeads = [...document.querySelectorAll(".chat-work-summary,.chat-step-summary,.chat-tool-group-head")].filter((node) => node.checkVisibility()).map(width);
    const chipRows = [...document.querySelectorAll(".thinking-line,.tool-tick")].filter((node) => node.checkVisibility()).map(width);
    return { speakers: document.querySelectorAll("#chat-log .chat-speaker").length, lefts: rows.map((row) => row.querySelector(":scope > .chat-content")?.getBoundingClientRect().left - log.left), transcriptWidth: width(document.querySelector(".chat-response-content")), proseRows, foldHeads, chipRows, names: [...document.querySelectorAll("#chat-log > .chat-entry")].map((row) => row.getAttribute("aria-label")) };
  }, pixels);
  const defaultLayout = await rowLayout(12); console.log("2tk default rows", JSON.stringify(defaultLayout));
  expect(defaultLayout.speakers).toBe(0); expect(new Set(defaultLayout.lefts)).toEqual(new Set([4]));
  expect(defaultLayout.proseRows.length, "finished prose rows are present").toBeGreaterThan(0); expect(new Set(defaultLayout.proseRows), "every finished prose row spans the transcript").toEqual(new Set([defaultLayout.transcriptWidth]));
  expect(defaultLayout.foldHeads.length, "finished fold heads are present").toBeGreaterThan(0); expect(defaultLayout.foldHeads.every((width) => width <= defaultLayout.transcriptWidth), "every fold head is max-content, never wider than the transcript").toBe(true);
  expect(defaultLayout.chipRows.length, "finished chip rows are present").toBeGreaterThan(0); expect(defaultLayout.chipRows.every((width) => width <= defaultLayout.transcriptWidth), "every chip row is max-content, never wider than the transcript").toBe(true);
  expect(defaultLayout.names).toEqual(expect.arrayContaining(["you", "agent_b", "summary"]));
  await finished.page.screenshot({ path: "test-results/2r2-default.png", fullPage: true });
  for (const [name, pixels] of [["smallest", 9], ["largest", 32]]) { const seen = await rowLayout(pixels); expect(seen.speakers).toBe(0); await finished.page.screenshot({ path: `test-results/2r2-${name}.png`, fullPage: true }); }
  await mkdir("logs/evidence/rel-1.62.33", { recursive: true });
  await finished.page.setViewportSize({ width: 1280, height: 2400 });
  for (const pixels of [9, 16, 32]) { await rowLayout(pixels); await finished.page.locator("#chat-log").evaluate((node) => { node.scrollTop = 0; }); await finished.page.screenshot({ path: `logs/evidence/rel-1.62.33/2rg-transcript-${pixels}px.png`, fullPage: true }); }
  for (const width of [304, 1280, 1920]) for (const pixels of [12, 32]) {
    await finished.page.setViewportSize({ width, height: pixels === 32 ? 3000 : 1600 }); await rowLayout(pixels); await finished.page.locator("#chat-log").evaluate((node) => { node.scrollTop = 0; });
    await finished.page.evaluate((show) => { const panel = document.querySelector(".chat-list-panel"), handle = document.querySelector(".chat-list-handle"); document.body.classList.toggle("chat-list-visible", show); document.body.classList.toggle("chat-list-hidden", !show); panel.hidden = !show; handle.hidden = show; }, width > 360);
    await finished.page.screenshot({ path: `logs/evidence/rel-1.62.33/2sl-mixed-${width}-${pixels}px.png`, fullPage: true });
  }
  await expect(finished.page.locator(".chat-response-author")).toHaveCount(0); await finished.context.close();

  const noRunState = snapshot("idle");
  noRunState.sessions[id].chat = noRunState.sessions[id].chat.map(({ run_id, ...entry }) => entry);
  const noRun = await open("idle", "no-run-2vk.test", noRunState);
  await expect(noRun.page.getByRole("button", { name: "Undo file changes" })).toHaveCount(0);
  await expect(noRun.page.locator(".chat-work-files-toggle")).toHaveCount(0);
  await noRun.context.close();

  const shellOnly = snapshot("idle"); shellOnly.sessions[id].chat = [replay[0], replay[1], replay[2], replay.at(-1)]; shellOnly.sessions[id].cursor.offset = 4;
  const shellPage = await open("idle", "shell-only-2rk.test", shellOnly);
  await expect(shellPage.page.locator(".chat-work-undo")).toHaveCount(0);
  await expect(shellPage.page.locator(".chat-work-summary")).not.toContainText("file tools only");
  await shellPage.context.close();

  const askState = snapshot("running");
  askState.sessions[id].pending_approval = { type: "notice", key: "ask-card", event: { type: "approval.required", data: { call_id: "ask-1", name: "ask", kind: "question", question: "Which result should be kept?", choices: ["First", "Second", "Both"] } } };
  const askPage = await open("running", "ask-2rj.test", askState);
  await expect(askPage.page.getByText("Which result should be kept?")).toBeVisible();
  await expect(askPage.page.locator(".approval-actions button")).toHaveText(["First", "Second", "Both", "Send"]);
  await expect(askPage.page.locator('.approval-actions input[placeholder="Your answer"]')).toBeVisible();
  await askPage.page.screenshot({ path: "logs/evidence/rel-1.62.35/captures/ask-1280x860.png", fullPage: true });
  await askPage.context.close();

  const live = await open("running", "live-2ql.test");
  await expect(live.page.locator(".chat-work-summary")).toHaveCount(0);
  const liveBlocks = live.page.locator(".chat-response-block");
  await expect(liveBlocks).toHaveCount(2);
  await expect(liveBlocks.nth(0).locator(".chat-step-rows > *")).toHaveCount(0);
  const hoverBox = await live.page.evaluate(() => { const box = document.querySelectorAll(".chat-response-block")[1].getBoundingClientRect(); return { x: box.x, y: box.y }; });
  await live.page.mouse.move(hoverBox.x + 4, hoverBox.y + 4);
  await live.page.evaluate((next) => { next.sessions["chat-2ql"].chat.push({ type: "agent", key: "hovered-next", run_id: "run-2ql", text: "Started another step.", done: true }, { type: "tool", key: "hovered-tool", run_id: "run-2ql", name: "read_file", args: { path: "C:\\synthetic\\next.txt" }, result: { ok: true, ms: 2 }, content: "next" }); next.sessions["chat-2ql"].cursor.offset += 50; globalThis.__fixtureEvents.emit("snapshot", { type: "snapshot", data: next }); }, snapshot("running"));
  await expect(liveBlocks.nth(1).locator(".chat-step-rows > *")).not.toHaveCount(0);
  const clickedTop = await liveBlocks.nth(0).locator(".chat-step-summary").evaluate((node) => { document.querySelector("#chat-log").scrollTop = document.querySelector("#chat-log").scrollHeight; return node.getBoundingClientRect().top; });
  await liveBlocks.nth(0).locator(".chat-step-summary").click();
  expect(Math.abs(await liveBlocks.nth(0).locator(".chat-step-summary").evaluate((node) => node.getBoundingClientRect().top) - clickedTop)).toBeLessThanOrEqual(1);
  await expect(liveBlocks.nth(0).locator(".chat-step-rows > *")).not.toHaveCount(0);
  for (let event = 0; event < 20; event++) await live.page.evaluate(({ next, event }) => { next.sessions["chat-2ql"].cursor.offset += event + 200; globalThis.__fixtureEvents.emit("snapshot", { type: "snapshot", data: next }); }, { next: snapshot("running"), event });
  await expect(liveBlocks.nth(0).locator(".chat-step-rows > *")).not.toHaveCount(0);
  await liveBlocks.nth(0).locator(".chat-step-summary").click();
  await expect(liveBlocks.nth(0).locator(".chat-step-rows > *")).toHaveCount(0);
  await expect(liveBlocks.nth(1).locator(".chat-step-rows > *")).not.toHaveCount(0);
  await liveBlocks.nth(1).locator(".chat-step-summary").click();
  await expect(liveBlocks.nth(1).locator(".chat-step-rows > *")).toHaveCount(0);
  await liveBlocks.nth(1).locator(".chat-step-summary").click();
  await expect(liveBlocks.nth(1).locator(".chat-step-rows > *")).not.toHaveCount(0);
  expect(await auditDisclosures(live.page)).toEqual([]);
  const liveStatus = live.page.locator("#chat-status-line");
  await expect(liveStatus).toBeVisible();
  await expect(liveStatus).toContainText(/read_file.*result\.txt/i);
  await expect(live.page.locator("#chat-log > :last-child")).toHaveAttribute("id", "chat-status-line");
  for (const size of [9, 12, 32]) {
    const clear = await live.page.evaluate((value) => { document.documentElement.style.setProperty("--ct", `${value}px`); document.querySelector("#chat-task").focus(); const box = document.querySelector(".chat-input-wrap").getBoundingClientRect(), stop = document.querySelector("#chat-send").getBoundingClientRect(); return { below: box.bottom - stop.bottom, right: box.right - stop.right }; }, size);
    expect(clear.below).toBeGreaterThanOrEqual(6); expect(clear.right).toBeGreaterThanOrEqual(6);
  }
  for (const zoom of [1, 1.5]) { await live.page.evaluate((value) => { document.body.style.zoom = value; document.documentElement.style.setProperty("--ct", "12px"); }, zoom); await live.page.screenshot({ path: `test-results/2so-focus-${zoom}.png`, fullPage: true }); }
  await live.page.evaluate(() => { document.body.style.zoom = 1; });
  const listEdge = await live.page.locator(".chat-list-resize").boundingBox();
  await live.page.mouse.move(listEdge.x + 2, 80); await live.page.mouse.down(); await live.page.mouse.move(0, 80); await live.page.mouse.up();
  for (const width of [304, 1280, 1920]) {
    await live.page.setViewportSize({ width, height: 760 });
    const gap = await liveBlocks.first().locator(".chat-narration-line").evaluate((row) => { const summary = row.querySelector(".chat-step-summary").getBoundingClientRect(), answer = row.querySelector(".chat-response-answer").getBoundingClientRect(); return innerWidth <= 600 ? summary.top - answer.bottom : summary.left - answer.right; });
    expect(gap).toBeGreaterThanOrEqual(7); expect(gap).toBeLessThanOrEqual(12);
    await live.page.screenshot({ path: `test-results/2sm-live-${width}.png`, fullPage: true });
  }
  await expect(live.page.locator("#chat-status-strip")).toHaveCount(0);
  await expect(live.page.locator(".chat-input-actions > .chat-attach-wrap + #chat-mic")).toHaveCount(1);
  await live.page.evaluate((next) => globalThis.__fixtureEvents.emit("snapshot", { type: "snapshot", data: next }), (() => {
    const next = structuredClone(snapshot("running"));
    next.sessions[id].chat.push({ type: "agent", key: "later-line", run_id: runID, text: "A newer synthetic line arrived.", done: false });
    next.sessions[id].cursor.offset += 400;
    return next;
  })());
  await expect(live.page.getByText("A newer synthetic line arrived.")).toBeVisible();
  await expect(live.page.locator("#chat-log > :last-child")).toHaveAttribute("id", "chat-status-line");
  await live.page.screenshot({ path: "test-results/2ql-s56-live.png", fullPage: true });
  await live.page.screenshot({ path: "test-results/2qm-running.png", fullPage: true });
  await live.context.close();
  const thoughtState = snapshot("running"), thought = { type: "agent", key: "plain-thought", run_id: runID, reasoning: "The complete thought stays here.", reasoningTokens: 2484, reasoningTokensEstimated: true, thinkingMS: 108300, text: "", done: false };
  thoughtState.sessions[id].chat = [replay[0], thought]; thoughtState.sessions[id].cursor.offset = 2;
  const thoughtPage = await open("running", "thought-2tr.test", thoughtState, "Review the synthetic build record."); await thoughtPage.page.setViewportSize({ width: 1280, height: 860 });
  const liveThoughtLine = thoughtPage.page.locator('[data-entry-key="thought:plain-thought"] .thinking-line'), beforeTop = await liveThoughtLine.evaluate((node) => node.getBoundingClientRect().top);
  thoughtState.sessions[id].chat = [replay[0], { ...thought, text: "Answer with thought.", done: true }, { type: "agent", key: "plain-answer", run_id: runID, text: "Answer without thought.", done: true }]; thoughtState.sessions[id].cursor.offset = 3; await thoughtPage.page.evaluate((next) => globalThis.__fixtureEvents.emit("snapshot", { type: "snapshot", data: next }), thoughtState);
  await expect(thoughtPage.page.getByText("Answer with thought.")).toBeVisible();
  const thoughtBlock = thoughtPage.page.locator('.chat-response-block').filter({ hasText: "Answer with thought." }), thoughtLine = thoughtBlock.locator('.thinking-line'); await expect(thoughtLine).toHaveCount(1);
  const thoughtProof = await thoughtBlock.evaluate((block) => { const line = block.querySelector('.thinking-line'), answer = block.querySelector('.chat-response-answer'), plain = [...document.querySelectorAll('.chat-response-answer')].find((node) => node.textContent.includes('Answer without')); const box = (node) => node.getBoundingClientRect(), style = (node) => { const seen = getComputedStyle(node); return [seen.color, seen.fontSize, seen.marginTop]; }; return { lineVisible: line.getClientRects().length > 0, lineAbove: box(line).bottom <= box(answer).top, aligned: Math.abs(box(line).left - box(answer).left) <= 1, chipVisible: [...block.querySelectorAll('.chat-step-summary')].some((node) => node.getClientRects().length), afterTop: box(line).top, styles: [style(answer), style(plain)] }; });
  console.log("2tr thought closed", JSON.stringify({ beforeTop, ...thoughtProof }));
  expect.soft(thoughtProof.lineVisible && thoughtProof.lineAbove && thoughtProof.aligned && thoughtProof.chipVisible, "thought line above and the finished-work chip remains reachable").toBe(true); expect.soft(thoughtProof.afterTop, "thought line did not move").toBeCloseTo(beforeTop, 1); expect.soft(thoughtProof.styles[0], "answer styling").toEqual(thoughtProof.styles[1]);
  await expect(thoughtLine).toContainText("thought · ~2,484 tokens · 108.3 s"); await mkdir("logs/evidence/rel-1.62.84/captures", { recursive: true }); await thoughtPage.page.screenshot({ path: "logs/evidence/rel-1.62.84/captures/2tr-thought-closed.png", fullPage: true });
  await thoughtLine.click(); const thoughtBody = thoughtBlock.locator('.thinking-body'), opened = await thoughtBody.isVisible(), bodyBetween = await thoughtBlock.evaluate((block) => { const line = block.querySelector('.thinking-line').getBoundingClientRect(), body = block.querySelector('.thinking-body').getBoundingClientRect(), answer = block.querySelector('.chat-response-answer').getBoundingClientRect(); return line.bottom <= body.top && body.bottom <= answer.top; }); await thoughtPage.page.screenshot({ path: "logs/evidence/rel-1.62.84/captures/2tr-thought-open.png", fullPage: true }); await thoughtLine.click(); expect.soft(opened && bodyBetween && !await thoughtBody.isVisible(), "one press opens in place and a second closes").toBe(true);
  console.log("2tr thought proof", JSON.stringify({ beforeTop, ...thoughtProof, bodyBetween }));
  await thoughtPage.context.close();
});

// Item 2lj (f) and (g): the standing UI contract still holds at every step. At
// the largest size nothing overflows and nothing gains a scrollbar it did not
// have, at the wide width and at the phone viewport both.
function chatPage(pixels, face) {
  return `<!doctype html><html><head><style>
    ${tokensCSS}
    :root { --bezel:#0D1014; --well:#080A0D; --ink:#E8EEF4; --mute:#707D8B; --trace:#F2B233; --alarm:#E4624F; --signal:#3CF08A;
      --accent-plan:#5AC8FA; --sans:sans-serif; --mono:monospace;
      --s1:4px; --s2:8px; --s3:12px; --s4:16px; --s6:24px; --s8:32px; --app-header-height:32px;
      --agent-tab-width:118px; --etch-fade:.165; --chat-size:${pixels}px; --chat-face:${face}; }
    ${appCSS}
    ${chatCSS}
  </style></head><body class="chat-page">
    <main id="chat-log" class="chat-log">
      <article class="chat-entry"><div class="chat-speaker">operator</div><div class="chat-content">
        <p>A message long enough to wrap at the phone width and to keep wrapping when the reader asks for the largest step.</p>
        <pre><code>read_file("C:/projects/agentb/internal/credential/store.go")</code></pre>
      </div></article>
    </main>
    <footer id="chat-composer" class="chat-composer">
      <div id="chat-resize-handle" class="chat-resize-handle"></div>
      <div class="chat-input-wrap"><textarea id="chat-input">typed text</textarea></div>
    </footer>
  </body></html>`;
}

for (const width of [1280, 390]) {
  for (const [step, pixels] of [["smallest", 9], ["largest", 32]]) {
    test(`the chat surface holds the UI contract at the ${step} step at ${width}px`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      await page.setContent(chatPage(pixels, '"OpenDyslexic", var(--sans)'));
      const findings = await page.evaluate(() => {
        const problems = [];
        if (document.documentElement.scrollWidth > document.documentElement.clientWidth + 1) problems.push("the page scrolls sideways");
        for (const element of document.querySelectorAll(".chat-composer, .chat-composer *, .chat-entry, .chat-content")) {
          const style = getComputedStyle(element);
          // The transcript scrolls because it must; nothing inside it may.
          if (/(auto|scroll)/.test(style.overflowY) && element.scrollHeight > element.clientHeight + 1) {
            problems.push(`${element.className || element.tagName} scrolls on its own`);
          }
          if (element.scrollWidth > element.clientWidth + 1) problems.push(`${element.className || element.tagName} overflows sideways`);
        }
        return problems;
      });
      expect(findings, findings.join("; ")).toEqual([]);
      expect(await page.locator(".chat-content").evaluate((node) => parseFloat(getComputedStyle(node).fontSize))).toBe(pixels);
      expect(await page.locator("#chat-input").evaluate((node) => parseFloat(getComputedStyle(node).fontSize))).toBeCloseTo(pixels * 7 / 6, 3);
    });
  }
}

// (e): a code block stays monospaced whatever the prose face is. The prose
// changes and the code does not, under every typeface the Chats page offers.
test("a code block stays monospaced under every typeface", async ({ page }) => {
  const faces = ["IBM Plex Sans", "Atkinson Hyperlegible", "OpenDyslexic", "Segoe UI", "Verdana", "Comic Sans MS"];
  for (const face of faces) {
    await page.setContent(chatPage(12, `"${face}", var(--sans)`));
    const seen = await page.evaluate(() => ({
      prose: getComputedStyle(document.querySelector(".chat-content p")).fontFamily,
      code: getComputedStyle(document.querySelector(".chat-content code")).fontFamily,
    }));
    expect(seen.prose, `${face} did not reach the prose`).toContain(face);
    expect(seen.code, `${face} leaked into a code block`).not.toContain(face);
    expect(seen.code).toContain("monospace");
  }
});

test("user bubbles hold short, long and attached content across the required range 2sl", async ({ page }) => {
  await mkdir("logs/evidence/rel-1.62.83/captures", { recursive: true });
  for (const width of [304, 1280, 1920]) {
    for (const pixels of [9, 12, 32]) {
      await page.setViewportSize({ width, height: width === 1280 ? 860 : 900 });
      await page.setContent(chatPage(pixels, '"IBM Plex Sans", sans-serif'));
      await page.locator("#chat-log").evaluate((log, markup) => { log.innerHTML = markup; }, `<article class="chat-entry chat-user" tabindex="0"><div class="chat-speaker" aria-label="you"><span></span></div><div class="chat-content chat-bubble"><div class="chat-user-text">short</div></div></article>
        <article class="chat-entry chat-user"><div class="chat-speaker" aria-label="you"><span></span></div><div class="chat-content chat-bubble"><div class="chat-user-text">${"x".repeat(400)}</div><pre><code>pasted block</code></pre><img alt="picture" width="40" height="30"><span class="file-chip">record.txt</span></div></article>
        <article class="chat-entry chat-agent"><div class="chat-speaker">agent_b</div><div class="chat-content"><p>left answer</p></div></article>
        <article class="chat-entry tool-entry"><div class="chat-speaker">tool</div><div class="chat-content">left tool</div></article>
        <article class="chat-entry chat-summary"><div class="chat-speaker" aria-label="summary" title="summary"><span></span></div><div class="chat-content">left summary</div></article>`);
      const seen = await page.evaluate(() => {
        const log = document.querySelector("#chat-log");
        const short = document.querySelectorAll(".chat-user")[0];
        const long = document.querySelectorAll(".chat-user")[1];
        const shortBubble = short.querySelector(".chat-content");
        const longBubble = long.querySelector(".chat-content");
        const input = document.querySelector(".chat-input-wrap");
        const bubbleStyle = getComputedStyle(shortBubble), inputStyle = getComputedStyle(input);
        const box = (node) => node.getBoundingClientRect();
        const range = document.createRange(); range.selectNodeContents(shortBubble.querySelector(".chat-user-text"));
        const textWidth = range.getBoundingClientRect().width;
        const padding = parseFloat(bubbleStyle.paddingLeft) + parseFloat(bubbleStyle.paddingRight);
        const descendantsInside = [...longBubble.querySelectorAll("*")].every((node) => box(node).left >= box(longBubble).left - 1 && box(node).right <= box(longBubble).right + 1);
        const selection = document.getSelection(); selection.removeAllRanges(); selection.addRange(range);
        short.focus();
        return {
          shortFits: box(shortBubble).width < box(short).width * .8,
          shortContentWidth: Math.abs(box(shortBubble).width - textWidth - padding),
          textInset: box(shortBubble.querySelector(".chat-user-text")).left - box(shortBubble).left,
          right: Math.abs(box(shortBubble).right - box(short).right),
          longRatio: box(longBubble).width / box(long).width,
          longOverflow: longBubble.scrollWidth - longBubble.clientWidth,
          transcriptOverflow: log.scrollWidth - log.clientWidth,
          descendantsInside,
          nestedVerticalScrollers: [...log.querySelectorAll("*")].filter((node) => /(auto|scroll)/.test(getComputedStyle(node).overflowY) && node.scrollHeight > node.clientHeight + 1).length,
          copied: selection.toString(),
          focusWhole: getComputedStyle(short).outlineStyle !== "none" && box(short).left >= 0 && box(short).right <= innerWidth,
          visuals: [bubbleStyle.backgroundColor, bubbleStyle.border, bubbleStyle.borderTopLeftRadius, bubbleStyle.borderBottomRightRadius, bubbleStyle.boxShadow],
          inputVisuals: [inputStyle.backgroundColor, inputStyle.borderRadius, inputStyle.boxShadow],
          leftRows: [...document.querySelectorAll(".chat-agent,.tool-entry,.chat-summary")].every((row) => getComputedStyle(row).display === "grid"),
          summary: document.querySelector(".chat-summary .chat-speaker").textContent,
        };
      });
      expect(seen.shortFits, `${width}px/${pixels}px short width`).toBe(true);
      expect(seen.shortContentWidth, `${width}px/${pixels}px short content width`).toBeLessThanOrEqual(2);
      expect(seen.textInset, `${width}px/${pixels}px text inset`).toBeGreaterThan(0);
      expect(seen.right, `${width}px/${pixels}px right edge`).toBeCloseTo(12, 1);
      expect(seen.longRatio, `${width}px/${pixels}px long width`).toBeLessThanOrEqual(.801);
      expect(seen.longOverflow, `${width}px/${pixels}px bubble overflow`).toBeLessThanOrEqual(1);
      expect(seen.transcriptOverflow, `${width}px/${pixels}px transcript overflow`).toBeLessThanOrEqual(1);
      expect(seen.descendantsInside, `${width}px/${pixels}px attached content`).toBe(true);
      expect(seen.nestedVerticalScrollers, `${width}px/${pixels}px nested scroller`).toBe(0);
      expect(seen.copied).toBe("short");
      expect(seen.focusWhole, `${width}px/${pixels}px focus ring`).toBe(true);
      expect(seen.visuals.slice(0, 2)).toEqual(["rgb(21, 24, 28)", "1px solid rgb(0, 0, 0)"]);
      expect(parseFloat(seen.visuals[2])).toBeCloseTo(pixels * 5 / 6, 1);
      expect(parseFloat(seen.visuals[3])).toBeCloseTo(pixels / 4, 1);
      expect(seen.visuals[4]).toBe("rgba(232, 238, 244, 0.1) 0px 1px 0px 0px inset, rgba(0, 0, 0, 0.38) 0px 3px 8px 0px");
      const picture = decodePNG(await page.locator(".chat-user .chat-content").first().screenshot()), pixel = (x, y) => [...picture.data.slice((y * picture.width + x) * 4, (y * picture.width + x) * 4 + 3)];
      const corners = [pixel(0, 0), pixel(picture.width - 1, 0), pixel(0, picture.height - 1), pixel(picture.width - 1, picture.height - 1)];
      expect(corners.every((colour) => Math.max(...colour) < 14), "rounded corners expose only the dark page and shadow").toBe(true); expect(pixel(Math.floor(picture.width / 2), 4)).toEqual([21,24,28]);
      const colours = ["E8EEF4","FFFFFF","FF6B6B","FF9F43","FFD166","3CF08A","4DDFF7","6CA8FF","C792EA","FF85C2"], luminance = (hex) => { const c = [0,2,4].map((at) => parseInt(hex.slice(at, at + 2), 16) / 255).map((v) => v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4); return .2126*c[0] + .7152*c[1] + .0722*c[2]; }, ratios = colours.map((colour) => (luminance(colour) + .05) / (luminance("15181C") + .05));
      expect(Math.min(...ratios)).toBeGreaterThanOrEqual(4.5); if (width === 1280 && pixels === 12) console.log("2tq bubble contrast", ratios.map((ratio) => ratio.toFixed(2)).join(", "));
      expect(seen.leftRows).toBe(true);
      expect(seen.summary).toBe("");
      if (pixels === 12 && width !== 1920) await page.screenshot({ path: `logs/evidence/rel-1.62.83/captures/2tq-user-bubbles-${width}.png`, fullPage: true });
    }
  }
});

test("reading menus draw every entry in its own face and colour and open at constant cost 2rg", async ({ page }) => {
  await page.setContent(`<!doctype html><style>
    :root{--bezel:#15181c;--ink:#e8eef4;--mute:#7d8794;--trace:#f2b233;--sans:sans-serif}
    ${appCSS}
  </style><div class="setting-menu-control font-menu"><button id="font-anchor">IBM Plex Sans</button><div id="font-menu" class="setting-menu" role="listbox" hidden></div></div>
  <div class="setting-menu-control colour-menu"><button>Default</button><div id="colour-menu" class="setting-menu" role="listbox"></div></div>`);
  const result = await page.evaluate(() => {
    const families = ["Arial", "Courier New", "Georgia", "Times New Roman", "Verdana", ...Array.from({ length: 495 }, (_, index) => `Synthetic Family ${String(index).padStart(3, "0")}`)];
    const fontMenu = document.querySelector("#font-menu");
    const fill = (count) => {
      fontMenu.replaceChildren(...families.slice(0, count).map((family) => {
        const button = document.createElement("button"); button.textContent = family; button.style.fontFamily = `"${family}", sans-serif`; return button;
      }));
    };
    const time = (count) => {
      fill(count); fontMenu.hidden = true; void fontMenu.offsetHeight;
      const start = performance.now(); fontMenu.hidden = false; void fontMenu.offsetHeight;
      return performance.now() - start;
    };
    const colours = { Default:"#E8EEF4", White:"#FFFFFF", Red:"#FF6B6B", Orange:"#FF9F43", Yellow:"#FFD166", Green:"#3CF08A", Cyan:"#4DDFF7", Blue:"#6CA8FF", Purple:"#C792EA", Pink:"#FF85C2" };
    const colourMenu = document.querySelector("#colour-menu");
    colourMenu.replaceChildren(...Object.entries(colours).map(([name, colour]) => { const button = document.createElement("button"); button.textContent = name; button.style.color = colour; return button; }));
    fill(500);
    const sampled = [...fontMenu.children].slice(0, 20).concat([...fontMenu.children].slice(-1)).map((node) => ({ name: node.textContent, face: getComputedStyle(node).fontFamily }));
    return { thirty: time(30), fiveHundred: time(500), sampled, colours: [...colourMenu.children].map((node) => getComputedStyle(node).color), overflow: getComputedStyle(fontMenu).overflowY };
  });
  console.log("2rg menu open ms", JSON.stringify({ thirty: result.thirty, fiveHundred: result.fiveHundred }));
  expect(result.thirty).toBeLessThan(100);
  expect(result.fiveHundred).toBeLessThan(100);
  expect(Math.abs(result.fiveHundred - result.thirty)).toBeLessThan(25);
  for (const row of result.sampled) expect(row.face).toContain(row.name);
  expect(result.colours).toEqual(["rgb(232, 238, 244)", "rgb(255, 255, 255)", "rgb(255, 107, 107)", "rgb(255, 159, 67)", "rgb(255, 209, 102)", "rgb(60, 240, 138)", "rgb(77, 223, 247)", "rgb(108, 168, 255)", "rgb(199, 146, 234)", "rgb(255, 133, 194)"]);
  expect(result.overflow).toBe("auto");
});

test("reading face and Pink affect prose and composer but preserve semantic colours 2rg", async ({ page }) => {
  await page.setContent(chatPage(16, '"A Family That Is Not Installed 2rg", sans-serif'));
  await page.locator("#chat-log").evaluate((log) => { log.innerHTML = `<article class="chat-entry chat-agent"><div class="chat-speaker">agent_b</div><div class="chat-content chat-response-answer"><p id="prose">answer</p><code id="code">code</code><a id="link">link</a><span id="alarm" class="alarm">error</span></div></article><article class="chat-entry tool-entry"><div class="chat-speaker">tool</div><div id="tool" class="chat-content">output</div></article>`; });
  const seen = await page.evaluate(() => {
    document.documentElement.style.setProperty("--chat-ink", "#FF85C2");
    const colour = (selector) => getComputedStyle(document.querySelector(selector)).color;
    const width = (family) => { const node = document.createElement("span"); node.textContent = "mmmmWWWWiiii"; node.style.cssText = `position:absolute;font:16px ${family}`; document.body.append(node); const value = node.getBoundingClientRect().width; node.remove(); return value; };
    return {
      prose: colour("#prose"), composer: colour("#chat-input"), code: colour("#code"), link: colour("#link"), alarm: colour("#alarm"), tool: colour("#tool"),
      missingWidth: width('"A Family That Is Not Installed 2rg", sans-serif'), defaultWidth: width("sans-serif"), codeFace: getComputedStyle(document.querySelector("#code")).fontFamily,
    };
  });
  expect(seen.prose).toBe("rgb(255, 133, 194)");
  expect(seen.composer).toBe("rgb(255, 133, 194)");
  expect(seen.code).not.toBe(seen.prose);
  expect(seen.link).not.toBe(seen.prose);
  expect(seen.alarm).not.toBe(seen.prose);
  expect(seen.tool).not.toBe(seen.prose);
  expect(seen.missingWidth).toBeCloseTo(seen.defaultWidth, 3);
  expect(seen.codeFace).toContain("monospace");
});

// Item 2lk (d): the same rule over EVERY settings page, not only Activity, so a
// new inner scroller is caught where it appears rather than the next time
// someone looks. The pages are rendered from their own source, so a selector
// that no page uses cannot hide here either.
const settingsSources = Object.fromEntries(await Promise.all(
  ["settings-security.js", "settings-general.js", "settings-workspace.js", "settings-profiles.js",
   "settings-connections.js", "settings-chats.js", "settings-notifications.js", "settings-about.js"]
    .map(async (name) => {
      try { return [name, await readFile(new URL(`../../web/js/${name}`, import.meta.url), "utf8")]; }
      catch { return [name, ""]; }
    })));

// Every class the settings pages put on an element, gathered from their own
// markup: whatever app.css says about any of them has to obey the contract.
const settingsClasses = [...new Set(
  Object.values(settingsSources).flatMap((source) => [...source.matchAll(/class="([^"$]+)"/g)]
    .flatMap((match) => match[1].split(/\s+/)))
)].filter(Boolean).sort();

test("no class any settings page uses is styled to scroll inside the page", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.setContent(`<!doctype html><html><head><style>
    :root { --bezel:#2A2E35; --well:#15181C; --ink:#D8DDE3; --mute:#7D8794; --trace:#F2B233; --alarm:#E4624F;
      --accent-plan:#5AC8FA; --sans:sans-serif; --mono:monospace;
      --s1:4px; --s2:8px; --s3:12px; --s4:16px; --s6:24px; --s8:32px; --app-header-height:32px; --agent-tab-width:118px; --etch-fade:.165; }
    ${appCSS}
  </style></head><body>
    <div id="settings-page" class="settings-page"><div class="settings-layout"><div class="settings-content">
      ${settingsClasses.map((name) => `<div class="${name}" data-probe="${name}">text</div>`).join("")}
    </div></div></div>
  </body></html>`);

  const offenders = await page.evaluate(() => [...document.querySelectorAll("[data-probe]")].filter((node) => {
    const style = getComputedStyle(node);
    return ["overflow", "overflow-y"].some((property) => ["auto", "scroll"].includes(style.getPropertyValue(property)));
  }).map((node) => node.dataset.probe));

  expect(offenders, "these settings classes scroll inside a page that already scrolls").toEqual([]);
  // The probe is only worth anything if it actually covered the two this item
  // exists for, so it says so rather than passing on an empty list.
  expect(settingsClasses).toContain("settings-feedback");
  expect(settingsClasses).toContain("memory-content");
});

// (b): Activity leaves no empty column at 1250px.
test("Activity's upper grid leaves no empty column at 1250px", async ({ page }) => {
  await page.setViewportSize({ width: 1250, height: 900 });
  await page.setContent(`<!doctype html><html><head><style>
    :root { --bezel:#2A2E35; --well:#15181C; --ink:#D8DDE3; --mute:#7D8794; --trace:#F2B233; --alarm:#E4624F;
      --accent-plan:#5AC8FA; --sans:sans-serif; --mono:monospace;
      --s1:4px; --s2:8px; --s3:12px; --s4:16px; --s6:24px; --s8:32px; --app-header-height:32px; --agent-tab-width:118px; --etch-fade:.165; }
    ${appCSS}
  </style></head><body>
    <div id="settings-page" class="settings-page"><div class="settings-layout"><div class="settings-content">${activityMarkup()}</div></div></div>
  </body></html>`);
  // Twenty metric rows on the right, two tool rows on the left: the shape that
  // produced the hole rel-1.14.0/W7 reported.
  await page.evaluate(() => {
    const line = (label, value) => `<div class="panel-line"><span>${label}</span><span>${value}</span></div>`;
    document.querySelector("#panel-stats").innerHTML =
      Array.from({ length: 20 }, (_, index) => line(`metric ${index}`, index)).join("");
    document.querySelector("#panel-tool-counters").innerHTML = line("list_dir", 1) + line("write_file", 1);
  });

  const geometry = await page.evaluate(() => {
    const pane = document.querySelector("#panel-tool-use").getBoundingClientRect();
    const lifetime = document.querySelector("#panel-lifetime").getBoundingClientRect();
    const toolTable = document.querySelector("#panel-tool-use table").getBoundingClientRect();
    const lifetimeTable = document.querySelector("#panel-lifetime table").getBoundingClientRect();
    return { pane: pane.height, paneWidth: Math.round(pane.width), lifetime: lifetime.height, width: Math.round(lifetime.width), toolTable: Math.round(toolTable.width), lifetimeTable: Math.round(lifetimeTable.width), sameRow: Math.abs(pane.top - lifetime.top) < 2 };
  });

  // Both panes take the whole surface, so there is no second column left to be
  // empty, and both headed tables use that width.
  expect(geometry.sameRow, "the panes still share a row").toBe(false);
  expect(geometry.width, "Lifetime does not take the width of the surface").toBeGreaterThan(700);
  expect(geometry.paneWidth, "Tool use does not take the width of the surface").toBeGreaterThan(700);
  expect(geometry.lifetimeTable).toBeGreaterThan(0);
  expect(geometry.lifetimeTable).toBeLessThanOrEqual(geometry.width);
  expect(geometry.toolTable).toBeGreaterThan(0);
  expect(geometry.toolTable).toBeLessThanOrEqual(geometry.paneWidth);
});

// Item 2ln (e): rel-1.15.0 card 8 said the Profiles page leaves a column empty at
// 1250px. [[2iq]] replaced that page with a role table in v1.16.0 and the card
// was carried forward UNMEASURED. This measures it, which is how the item says
// to close it: either the column is no longer empty, or it is fixed here.
test("the Profiles page leaves no empty column at 1250px", async ({ page }) => {
  await page.setViewportSize({ width: 1250, height: 900 });
  const agentsMarkup = (() => {
    const start = indexHTML.indexOf('<div id="agents-panel"');
    const end = indexHTML.indexOf('<div id="activity-panel"', start);
    return indexHTML.slice(start, end > start ? end : indexHTML.length);
  })();
  await page.setContent(`<!doctype html><html><head><style>
    :root { --bezel:#2A2E35; --well:#15181C; --ink:#D8DDE3; --mute:#7D8794; --trace:#F2B233; --alarm:#E4624F;
      --accent-plan:#5AC8FA; --sans:sans-serif; --mono:monospace;
      --s1:4px; --s2:8px; --s3:12px; --s4:16px; --s6:24px; --s8:32px; --app-header-height:32px; --agent-tab-width:118px; --etch-fade:.165; }
    ${appCSS}
  </style></head><body>
    <div id="settings-page" class="settings-page"><div class="settings-layout"><div class="settings-content">${agentsMarkup}</div></div></div>
  </body></html>`);

  // The shape that produced the hole: a role table of three rows beside a tool
  // list, in a two-column surface.
  await page.evaluate(() => {
    const row = (role, what) => `<div class="panel-role-row"><span class="panel-role-name">${role}</span><span class="panel-role-what">${what}</span><select><option>local</option></select><span class="panel-role-state">takes effect on the next run that reads it</span></div>`;
    document.querySelector("#panel-roles").innerHTML =
      row("b", "the one you talk to") + row("c", "the worker") + row("d", "the planner");
    document.querySelector("#panel-tools").innerHTML =
      Array.from({ length: 13 }, (_, i) => `<div class="panel-line"><span>tool ${i}</span><span>on</span></div>`).join("");
  });

  const geometry = await page.evaluate(() => {
    const surface = document.querySelector("#agents-panel");
    const kids = [...surface.children].map((node) => {
      const r = node.getBoundingClientRect();
      return { id: node.id || node.className, x: Math.round(r.left), w: Math.round(r.width), h: Math.round(r.height) };
    });
    return { surface: Math.round(surface.getBoundingClientRect().width), kids };
  });

  // Every child takes the width it is given: no child sits in one column of a
  // two-column surface with nothing beside it.
  const narrow = geometry.kids.filter((kid) => kid.w < geometry.surface - 4);
  expect(narrow, `these sit in a partial column: ${JSON.stringify(narrow)} of ${geometry.surface}px`).toEqual([]);
});
