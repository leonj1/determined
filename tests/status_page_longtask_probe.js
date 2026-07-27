// Long-task probe for the interactive status page. Point it at a running
// session (cmd/hangrepro or a real `determined -interactive ... -exec`) and it
// watches the page in headless Chrome for main-thread stalls — the direct
// cause of the browser's "wait or kill the page?" dialog, which Chrome shows
// after the main thread stays blocked for several seconds.
//
//   node tests/status_page_longtask_probe.js <url> [seconds]
//
// It reports PerformanceObserver longtask entries (tasks >50ms) and
// requestAnimationFrame gaps, and prints a PASS/FAIL verdict: a single task or
// frame gap above one second, or a main thread blocked for over a third of the
// observation window, is the freeze the bug report describes — Chrome's "wait
// or kill the page?" dialog fires once such blocking is sustained.
const { chromium } = require("playwright-core");

const url = process.argv[2];
const seconds = Number(process.argv[3] || 30);
if (!url) {
  console.error("usage: node tests/status_page_longtask_probe.js <url> [seconds]");
  process.exit(2);
}

async function main() {
  const browser = await chromium.launch({
    executablePath: "/usr/bin/google-chrome",
    headless: true,
  });
  const page = await browser.newPage();
  await page.addInitScript(() => {
    window.__probe = { longTasks: [], frameGaps: [] };
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        window.__probe.longTasks.push({ start: entry.startTime, duration: entry.duration });
      }
    }).observe({ type: "longtask", buffered: true });
    let last = performance.now();
    const tick = (now) => {
      if (now - last > 250) window.__probe.frameGaps.push({ start: last, gap: now - last });
      last = now;
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  });
  await page.goto(url.replace(/\/$/, "") + "/exec", { waitUntil: "domcontentloaded" });
  await page.waitForTimeout(seconds * 1000);
  const probe = await page.evaluate(() => window.__probe);
  await browser.close();
  report(probe, seconds);
}

function report(probe, seconds) {
  const durations = probe.longTasks.map((t) => t.duration);
  const gaps = probe.frameGaps.map((g) => g.gap);
  const total = durations.reduce((a, b) => a + b, 0);
  const max = Math.max(0, ...durations);
  const maxGap = Math.max(0, ...gaps);
  const blockedFraction = total / (seconds * 1000);
  console.log(`long tasks (>50ms): ${durations.length}`);
  console.log(`  total blocked: ${Math.round(total)}ms (${Math.round(blockedFraction * 100)}% of window)`);
  console.log(`  longest task:  ${Math.round(max)}ms`);
  console.log(`frame gaps (>250ms): ${gaps.length}, longest ${Math.round(maxGap)}ms`);
  const frozen = max > 1000 || maxGap > 1000 || blockedFraction > 0.33;
  console.log(frozen ? "FAIL: main thread froze for >1s — this is the hang" : "PASS: no freeze observed");
  process.exit(frozen ? 1 : 0);
}

main().catch((error) => {
  console.error(error);
  process.exit(2);
});
