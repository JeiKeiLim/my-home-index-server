import { test, expect, Page } from '@playwright/test';
import { spawn, ChildProcess } from 'node:child_process';
import { E2E_CONFIG } from '../playwright.config';
import { waitForPort } from '../helpers';

// Per-spec X-Forwarded-For so the server's TRUST_XFF=true rate-limit
// bucket cannot be tripped by S8's intentional exhaustion in
// login.spec.ts. See README.md security section.
test.use({
  extraHTTPHeaders: {
    'X-Forwarded-For': `e2e-copy-mobile-${process.pid}-${Math.random().toString(36).slice(2)}`,
  },
});

// ──────────────────────────────────────────────────────────────────────────
// S5 — copy URL and copy CWD
// S6 — mobile responsive layout
// ──────────────────────────────────────────────────────────────────────────

const SPAWNED: ChildProcess[] = [];

function spawnListener(port: number) {
  const child = spawn('python3', ['-c', `
import http.server, socketserver, signal, sys
signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
socketserver.TCPServer(('0.0.0.0', ${port}), http.server.SimpleHTTPRequestHandler).serve_forever()
`], { stdio: 'ignore', detached: false });
  SPAWNED.push(child);
  return child;
}

async function login(page: Page) {
  await page.goto('/login');
  await page.fill('input[name="token"]', E2E_CONFIG.AUTH_TOKEN);
  await page.click('button[type="submit"]');
  await expect(page).toHaveURL(/^http:\/\/localhost:\d+\/$/);
}

test.afterAll(async () => { for (const c of SPAWNED) { try { c.kill('SIGKILL'); } catch {} } });

test('S5: copy URL writes expected public URL to clipboard', async ({ page, context, browserName }) => {
  if (browserName === 'webkit') test.skip(true, 'clipboard permissions differ on webkit');
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  const port = E2E_CONFIG.PORT_RANGE_MIN + 5; // 40195
  spawnListener(port);

  await login(page);
  // waitForPort polls /ports.json until the listener is observed alive,
  // eliminating the htmx 2s poll + 1s scan race that flaked S5/S6.
  await waitForPort(page, port);
  const row = page.locator(`tr:has-text("${port}")`).first();
  await expect(row).toBeVisible({ timeout: 5000 });
  await row.locator('button:has-text("copy url")').first().click();

  const clip = await page.evaluate(() => navigator.clipboard.readText());
  // Spec fixes PUBLIC_HOST to "localhost" for e2e
  expect(clip).toBe(`http://localhost:${port}/`);
});

test('S5: copy CWD writes an absolute path to clipboard', async ({ page, context, browserName }) => {
  if (browserName === 'webkit') test.skip(true, 'clipboard permissions differ on webkit');
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  const port = E2E_CONFIG.PORT_RANGE_MIN + 6; // 40196
  spawnListener(port);

  await login(page);
  await waitForPort(page, port);
  const row = page.locator(`tr:has-text("${port}")`).first();
  await expect(row).toBeVisible({ timeout: 5000 });
  // Click the inline ⎘ (on desktop) or the "copy cwd" button (on mobile).
  // `.copy` matches the desktop <span class="copy"> affordance directly
  // (webkit's attribute-selector matching for `[title="copy cwd"]` is
  // less reliable than a plain class selector); the button variant
  // covers the mobile card. Use force:true because the desktop ⎘ span
  // can be visually clipped by the cwd cell's overflow:hidden ellipsis
  // when the path is long — the click event still dispatches correctly.
  const copyBtn = row.locator('.copy, button:has-text("copy cwd")').first();
  await copyBtn.click({ force: true });

  const clip = await page.evaluate(() => navigator.clipboard.readText());
  // Must be absolute (starts with /), not "~/"
  expect(clip.startsWith('/')).toBe(true);
  expect(clip.length).toBeGreaterThan(1);
});

test('S6: mobile viewport shows stacked cards, hides table', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const port = E2E_CONFIG.PORT_RANGE_MIN + 7; // 40197
  spawnListener(port);

  await login(page);
  await waitForPort(page, port);
  const card = page.locator(`.card:has-text("${port}")`);
  // The card must be actually rendered (visible), not just present in the
  // DOM — this catches the HTML5 foster-parenting bug where the OOB
  // <div id="rows-mobile"> gets hoisted into <tbody> as an invisible 0x0
  // element and the real mobile container stays empty.
  await expect(card).toBeVisible({ timeout: 5000 });
  await expect(page.locator('table')).toBeHidden({ timeout: 5000 });

  const tableDisplay = await page.evaluate(
    () => getComputedStyle(document.querySelector('table') as Element).display,
  );
  expect(tableDisplay).toBe('none');

  // There must be exactly one #rows-mobile in the live tree — foster-parenting
  // used to produce a second empty copy hoisted above <tbody>.
  const rowsMobileCount = await page.locator('#rows-mobile').count();
  expect(rowsMobileCount).toBe(1);
});

test('S6 (inverse): desktop viewport shows table, hides cards', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  const port = E2E_CONFIG.PORT_RANGE_MIN + 8; // 40198
  spawnListener(port);

  await login(page);
  await waitForPort(page, port);
  const row = page.locator(`tr:has-text("${port}")`).first();
  await expect(row).toBeVisible({ timeout: 5000 });
  await expect(page.locator('table')).toBeVisible({ timeout: 5000 });

  // .cards wrapper collapses at ≥820px. Individual .card nodes are still
  // in the DOM (OOB-swapped into #rows-mobile), but must not render.
  const cardsDisplay = await page.evaluate(
    () => getComputedStyle(document.querySelector('#rows-mobile') as Element).display,
  );
  expect(cardsDisplay).toBe('none');
});

test('S5: copy-url click shows a toast banner', async ({ page, context, browserName }) => {
  if (browserName === 'webkit') test.skip(true, 'clipboard permissions differ on webkit');
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  const port = E2E_CONFIG.PORT_RANGE_MIN + 4; // 40194
  spawnListener(port);

  await login(page);
  await waitForPort(page, port);
  const row = page.locator(`tr:has-text("${port}")`).first();
  await expect(row).toBeVisible({ timeout: 5000 });
  await row.locator('button:has-text("copy url")').first().click();

  // Toast must appear promptly and auto-dismiss (2s show + 250ms fade + removal).
  const toast = page.locator('.toast.show').first();
  await expect(toast).toBeVisible({ timeout: 2000 });
  await expect(toast).toContainText(`:${port}/`);
  // The 2000ms setTimeout that removes "show" runs on the page's event
  // loop, which can be preempted by concurrent htmx /ports polls in a
  // busy run (the dismiss setTimeout may fire a few hundred ms late).
  // Give the 2s dismiss timer + 250ms fade a generous slack so a
  // background-throttled or preempted timer still lands inside the
  // window. The assertion still fails loudly if the toast never
  // dismisses, so robustness here doesn't mask real regressions.
  await expect(page.locator('.toast.show')).toHaveCount(0, { timeout: 8000 });
});
