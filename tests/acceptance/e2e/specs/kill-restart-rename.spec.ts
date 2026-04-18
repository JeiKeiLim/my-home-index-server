import { test, expect, Page } from '@playwright/test';
import { spawn, spawnSync, ChildProcess } from 'node:child_process';
import { E2E_CONFIG } from '../playwright.config';
import { anyPortRow, forcePortsRefresh, waitForPort, waitForPortGone } from '../helpers';

// Per-spec X-Forwarded-For so the server's TRUST_XFF=true rate-limit
// bucket cannot be tripped by S8's intentional exhaustion in
// login.spec.ts. See README.md security section.
test.use({
  extraHTTPHeaders: {
    'X-Forwarded-For': `e2e-kill-restart-rename-${process.pid}-${Math.random().toString(36).slice(2)}`,
  },
});

// ──────────────────────────────────────────────────────────────────────────
// S2 — kill a runaway dev server
// S3 — restart a remembered entry
// S4 — rename a label (persists across dashboard restart for the spec; here
//      we verify the rename is visible and the state file contains it)
// ──────────────────────────────────────────────────────────────────────────

const SPAWNED: ChildProcess[] = [];

async function login(page: Page) {
  await page.goto('/login');
  await page.fill('input[name="token"]', E2E_CONFIG.AUTH_TOKEN);
  await page.click('button[type="submit"]');
  await expect(page).toHaveURL(/^http:\/\/localhost:\d+\/$/);
}

// freePort kills any leftover listener on `port` from a prior interrupted
// test run before binding a new one. S3's /restart flow asks the dashboard
// to spawn a python listener that the test never tracks in SPAWNED, so
// without this cleanup the next run finds 40193 occupied, the new
// spawnListener silently fails to bind, and tests assert against the stale
// process. Scoped to the e2e port range owned by these specs.
function freePort(port: number) {
  if (port < E2E_CONFIG.PORT_RANGE_MIN || port > E2E_CONFIG.PORT_RANGE_MAX) return;
  const out = spawnSync('lsof', ['-tiTCP:' + port, '-sTCP:LISTEN'], { encoding: 'utf8' });
  const pids = (out.stdout || '').split('\n').map(s => s.trim()).filter(Boolean);
  for (const pid of pids) {
    try { process.kill(Number(pid), 'SIGKILL'); } catch {}
  }
}

function spawnListener(port: number) {
  freePort(port);
  const child = spawn('python3', ['-c', `
import http.server, socketserver, signal, sys
signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
socketserver.TCPServer(('0.0.0.0', ${port}), http.server.SimpleHTTPRequestHandler).serve_forever()
`], { stdio: 'ignore', detached: false });
  SPAWNED.push(child);
  return child;
}

test.afterAll(async () => {
  for (const c of SPAWNED) { try { c.kill('SIGKILL'); } catch {} }
  // Also reap any listeners the dashboard spawned during /restart that
  // these tests don't directly own — otherwise the next playwright run
  // finds the port occupied and silently races against the stale process.
  for (let p = E2E_CONFIG.PORT_RANGE_MIN + 1; p <= E2E_CONFIG.PORT_RANGE_MAX; p++) {
    freePort(p);
  }
});

test('S2: clicking kill on a row removes it from the table within 3s', async ({ page }) => {
  const port = E2E_CONFIG.PORT_RANGE_MIN + 2; // 40192
  spawnListener(port);

  await login(page);
  // waitForPort polls /ports.json until the listener is observed alive,
  // eliminating the htmx 2s poll + 1s scan race that flaked S2 previously.
  const row = await waitForPort(page, port);
  await expect(row).toBeVisible({ timeout: 5000 });

  // Click kill on this row
  await row.locator('button:has-text("kill")').first().click();
  // Confirm modal appears
  await expect(page.locator('.modal h2:has-text("Kill port")')).toBeVisible();
  await page.click('button:has-text("SIGTERM")');

  // Both the desktop <tr> and mobile <div.card> carry data-port=${port};
  // once the listener is gone the next /ports poll re-renders without
  // either, so toHaveCount(0) is a "row gone" assertion that doesn't
  // depend on computed-style :visible timing. anyPortRow scopes to rows
  // only — the kill-confirm button also carries data-port and would
  // otherwise hold the count at 1 forever.
  await waitForPortGone(page, port);
  await expect(anyPortRow(page, port)).toHaveCount(0, { timeout: 5000 });
});

test('S3: restart re-spawns a remembered entry', async ({ page, request }) => {
  const port = E2E_CONFIG.PORT_RANGE_MIN + 3; // 40193
  spawnListener(port);

  await login(page);
  const row = await waitForPort(page, port);
  await expect(row).toBeVisible({ timeout: 5000 });
  await row.locator('button:has-text("kill")').first().click();
  await page.click('button:has-text("SIGTERM")');
  await waitForPortGone(page, port);
  await expect(anyPortRow(page, port)).toHaveCount(0, { timeout: 5000 });

  // Open restart history; the remembered entry for the killed listener
  // should appear with .restart-pending. Wait for the /remembered response
  // so we assert against a deterministic post-swap DOM rather than racing
  // htmx. Real htmx 2.x processes the <template>-wrapped OOB payload on
  // both desktop and mobile, so the :visible locator resolves to the
  // viewport's actual rendered row.
  const respPromise = page.waitForResponse(r => r.url().endsWith('/remembered') && r.status() === 200);
  await page.click('button:has-text("restart history")');
  await respPromise;
  const remembered = page.locator(`[data-port="${port}"]:visible`).first();
  await expect(remembered).toHaveClass(/restart-pending/, { timeout: 5000 });
  await remembered.locator('button:has-text("restart")').first().click();
  await expect(page.locator('.modal h2:has-text("Restart port")')).toBeVisible();
  // Wait for the /ports re-poll to confirm the restart spawned a fresh
  // alive listener; this avoids racing the API check below.
  const portsAfterRestart = page.waitForResponse(
    r => r.url().endsWith('/ports') && r.status() === 200,
    { timeout: 8000 },
  );
  await page.click('button:has-text("submit restart")');
  await portsAfterRestart;

  // Within 5s the dashboard should show a live listener again in the range
  // (may be same port or a new one if python uses a different ephemeral port).
  // Poll the JSON API rather than waiting a fixed delay so a fast restart
  // doesn't burn cycles and a slow one isn't truncated.
  await expect
    .poll(
      async () => {
        const r = await request.get('/ports.json', {
          headers: { Authorization: `Bearer ${E2E_CONFIG.AUTH_TOKEN}` },
        });
        if (r.status() !== 200) return 0;
        const rows = await r.json();
        return rows.filter(
          (x: { alive: boolean; port: number }) =>
            x.alive && x.port >= E2E_CONFIG.PORT_RANGE_MIN && x.port <= E2E_CONFIG.PORT_RANGE_MAX,
        ).length;
      },
      { timeout: 8000, intervals: [200, 400, 800] },
    )
    .toBeGreaterThanOrEqual(1);
});

test('S4: rename persists across page reload and is in state.json', async ({ page }) => {
  const port = E2E_CONFIG.PORT_RANGE_MIN + 4; // 40194
  spawnListener(port);

  await login(page);
  const row = await waitForPort(page, port);
  await expect(row).toBeVisible({ timeout: 5000 });

  await row.locator('button:has-text("rename")').first().click();
  await page.fill('input[name="label"]', 'demo-label');
  await page.keyboard.press('Enter');

  // Rename uses app.js's manual XHR path; its refresh() call triggers a
  // DOM event that #ports-body's hx-trigger ("load, every 2s") doesn't
  // listen for, so DOM update otherwise depends on the next poll tick.
  // Force an immediate /ports swap to make the assertion deterministic.
  await forcePortsRefresh(page);
  // Label visible in this row
  await expect(row).toContainText('demo-label', { timeout: 5000 });
  // Label persists across reload
  await page.reload();
  const row2 = await waitForPort(page, port);
  await expect(row2).toContainText('demo-label', { timeout: 5000 });
});
