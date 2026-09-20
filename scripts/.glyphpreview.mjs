import { chromium } from "@playwright/test";
import { svg } from "file:///C:/Users/Randy/AppData/Local/Temp/claude/C--projects-agentb/e535926c-a317-44a3-a1c3-ab860df130a6/scratchpad/brain.mjs";
const browser = await chromium.launch({ channel: "msedge", headless: true });
const page = await browser.newPage({ viewport: { width: 420, height: 160 } });
await page.setContent(`<body style="margin:0;background:#2A2E35;display:flex;gap:28px;align-items:center;justify-content:center;height:160px">
<span style="display:inline-flex;padding:2px 6px;background:rgba(216,221,227,.08);border-radius:2px">${svg()}</span>
<span class="sel" style="display:inline-flex;padding:2px 6px;background:rgba(216,221,227,.16);border-radius:2px">${svg()}</span>
<span style="transform:scale(6);transform-origin:center">${svg()}</span>
<style>.shell-page-icon{display:block;width:16px;height:16px;fill:none;stroke:currentColor;stroke-width:1.5;stroke-linecap:round;stroke-linejoin:round}
.shell-page-brain{stroke:#5AC8FA;stroke-width:1.4;opacity:.45}.sel .shell-page-brain{opacity:1}</style></body>`);
await page.screenshot({ path: process.argv[2] });
await browser.close();
