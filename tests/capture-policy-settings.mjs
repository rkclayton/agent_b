import { chromium } from "playwright";

const [url, output, wording] = process.argv.slice(2);
if (!url || !output || !wording) throw new Error("usage: capture-policy-settings.mjs <url> <output> <wording>");
const browser = await chromium.launch({ channel: "msedge", headless: true });
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 720 } });
  await page.goto(url);
  if (page.url().includes("/setup")) await page.locator(".setup-settings").click();
  await page.waitForURL(/\/chat/);
  await page.evaluate(() => document.dispatchEvent(new CustomEvent("settings.open", { detail: { section: "shell" } })));
  const policyLine = page.getByText(wording, { exact: true });
  try {
    await policyLine.waitFor({ timeout: 10_000 });
  } catch (error) {
    await page.screenshot({ path: output });
    console.error(`capture URL: ${page.url()}\n${(await page.locator("body").innerText()).slice(0, 4000)}`);
    throw error;
  }
  await policyLine.scrollIntoViewIfNeeded();
  await page.screenshot({ path: output });
} finally {
  await browser.close();
}
