import { chromium } from "playwright";
import { readFileSync } from "node:fs";

const [, , a, b, x0, y0, x1, y1, out] = process.argv;
const pad = 6;
const box = { x: Math.max(0, +x0 - pad), y: Math.max(0, +y0 - pad), w: +x1 - +x0 + pad * 2 + 1, h: +y1 - +y0 + pad * 2 + 1 };
const browser = await chromium.launch({ channel: "msedge" });
const page = await browser.newPage({ viewport: { width: box.w * 4, height: box.h * 8 + 40 } });
const url = (p) => `data:image/png;base64,${readFileSync(p).toString("base64")}`;
await page.setContent(`<body style="margin:0;background:#111">
 <canvas id=c width=${box.w * 4} height=${box.h * 8}></canvas>
 <script>
 window.draw = async (srcs) => {
  const ctx = document.getElementById('c').getContext('2d');
  ctx.imageSmoothingEnabled = false;
  for (let i = 0; i < srcs.length; i++) {
    const img = new Image(); img.src = srcs[i];
    await img.decode();
    ctx.drawImage(img, ${box.x}, ${box.y}, ${box.w}, ${box.h}, 0, i * ${box.h * 4}, ${box.w * 4}, ${box.h * 4});
  }
 };
 </script></body>`);
await page.evaluate((s) => window.draw(s), [url(a), url(b)]);
await page.locator("#c").screenshot({ path: out });
await browser.close();
console.log(out);
