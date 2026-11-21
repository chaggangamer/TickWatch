// Captures the four README screenshots into docs/screenshots.
// Run against a seeded, isolated instance:
//
//   docker run --rm \
//     -v "$(pwd -W):/work" -w /work \
//     -e SHOTS_BASE=http://host.docker.internal:8081 \
//     -e SHOTS_ADMIN_PASSWORD=... \
//     mcr.microsoft.com/playwright:v1.49.1-jammy \
//     node scripts/screenshots.js
//
// The admin password is passed through the environment and never stored.
const { chromium } = require("playwright");

const base = process.env.SHOTS_BASE || "http://host.docker.internal:8081";
const password = process.env.SHOTS_ADMIN_PASSWORD;
if (!password) {
  console.error("SHOTS_ADMIN_PASSWORD not set");
  process.exit(1);
}

(async () => {
  const browser = await chromium.launch();
  const context = await browser.newContext({
    viewport: { width: 1280, height: 800 },
    deviceScaleFactor: 2,
  });
  const page = await context.newPage();

  await page.goto(base + "/login");
  await page.fill("#username", "admin");
  await page.fill("#password", password);
  await page.click("button[type=submit]");
  await page.waitForURL(base + "/");
  await page.waitForTimeout(1500);
  await page.screenshot({ path: "docs/screenshots/dashboard.png" });

  await page.goto(base + "/monitors/1");
  await page.waitForTimeout(2000);
  await page.screenshot({ path: "docs/screenshots/monitor.png" });

  await page.goto(base + "/status");
  await page.waitForTimeout(1000);
  await page.screenshot({ path: "docs/screenshots/status.png" });

  await page.emulateMedia({ colorScheme: "dark" });
  await page.goto(base + "/");
  await page.waitForTimeout(1000);
  await page.screenshot({ path: "docs/screenshots/dark.png" });

  await browser.close();
  console.log("screenshots written");
})();
