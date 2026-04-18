import { test, expect } from '@playwright/test';
import { E2E_CONFIG } from '../playwright.config';
import { waitForPort } from '../helpers';

// Each spec file rotates its X-Forwarded-For so that the rate-limit
// bucket on the server (TRUST_XFF=true) is unique per file. S8
// intentionally exhausts the 5/15min budget for the configured remote
// IP — without this isolation the failure cascades into S1/S2/S3/S4/S6
// across both Playwright projects. See README.md security section.
test.use({
  extraHTTPHeaders: {
    'X-Forwarded-For': `e2e-login-${process.pid}-${Math.random().toString(36).slice(2)}`,
  },
});

// ──────────────────────────────────────────────────────────────────────────
// S1 — first-time login + discovery
// S7 — auth guard on mutation routes
// S8 — rate-limit lockout
// ──────────────────────────────────────────────────────────────────────────

test('S1: unauthenticated GET / redirects to /login', async ({ page }) => {
  const resp = await page.goto('/', { waitUntil: 'commit' });
  await expect(page).toHaveURL(/\/login/);
  expect(resp?.status()).toBeLessThan(400);
});

test('S1: login with correct token lands on dashboard and shows seeded port', async ({ page }) => {
  await page.goto('/login');
  await page.fill('input[name="token"]', E2E_CONFIG.AUTH_TOKEN);
  await page.click('button[type="submit"]');
  // Must land on /, not bounce back to /login
  await expect(page).toHaveURL(/^http:\/\/localhost:\d+\/$/);
  // Seeded listener on 40190 must show up. Wait for /ports.json to
  // confirm the scanner sees it before asserting DOM count, which makes
  // the test deterministic against the htmx 2s poll + 1s scan race.
  // Use [data-port] which lives on both the desktop <tr> and mobile
  // <div.card>; this avoids strict-mode violation on a bare text match
  // (the port number also appears in the header range, inside argv
  // strings, etc.) and implicitly verifies BOTH viewports rendered the
  // row (count=2).
  await waitForPort(page, E2E_CONFIG.PORT_RANGE_MIN);
  await expect(
    page.locator(`[data-port="${E2E_CONFIG.PORT_RANGE_MIN}"]`),
  ).toHaveCount(2, { timeout: 5000 });
  // Session persists across reload
  await page.reload();
  await expect(page).toHaveURL(/^http:\/\/localhost:\d+\/$/);
});

test('S1: dashboard excludes its own PID', async ({ page, request }) => {
  await page.goto('/login');
  await page.fill('input[name="token"]', E2E_CONFIG.AUTH_TOKEN);
  await page.click('button[type="submit"]');
  await expect(page).toHaveURL(/^http:\/\/localhost:\d+\/$/);
  // The dashboard port itself (E2E_CONFIG.DASHBOARD_PORT) must NOT be in /ports.json
  const r = await request.get('/ports.json', {
    headers: { Authorization: `Bearer ${E2E_CONFIG.AUTH_TOKEN}` },
  });
  expect(r.status()).toBe(200);
  const rows = await r.json();
  expect(Array.isArray(rows)).toBe(true);
  for (const row of rows) {
    expect(row.port).not.toBe(E2E_CONFIG.DASHBOARD_PORT);
  }
});

test('S7: POST /kill without auth returns 401', async ({ request }) => {
  const r = await request.post('/kill/40190');
  expect(r.status()).toBe(401);
});

test('S7: POST /kill with wrong bearer returns 401', async ({ request }) => {
  const r = await request.post('/kill/40190', {
    headers: { Authorization: 'Bearer wrong-token' },
  });
  expect(r.status()).toBe(401);
});

test('S8: 5 failed logins followed by 6th returns 429', async ({ request }) => {
  // Five bad attempts
  for (let i = 0; i < 5; i++) {
    const r = await request.post('/login', { form: { token: 'wrong' } });
    expect([200, 401, 303]).toContain(r.status()); // form-render, auth-fail, or redirect
  }
  // Sixth attempt (even with correct token) must be rate-limited
  const r6 = await request.post('/login', { form: { token: E2E_CONFIG.AUTH_TOKEN } });
  expect(r6.status()).toBe(429);
  expect(r6.headers()['retry-after']).toBeTruthy();
});
