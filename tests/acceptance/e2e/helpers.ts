import { Page, Locator, expect } from '@playwright/test';
import { E2E_CONFIG } from './playwright.config';

// anyPortRow matches every row representation for a given port — both the
// desktop <tr> and the mobile <div.card> — but NOT other elements that
// happen to carry a stale data-port attribute (notably the kill-confirm
// modal button, which app.js tags with data-port when the modal opens and
// does not clean up on close). Use this for "row gone" assertions via
// toHaveCount(0) where computed-style :visible racing would produce
// flakes.
export function anyPortRow(page: Page, port: number): Locator {
  return page.locator(`tr[data-port="${port}"], .card[data-port="${port}"]`);
}

// forcePortsRefresh drives htmx.ajax to fetch /ports into #ports-body
// immediately, bypassing the 2s polling tick. Needed because the kill
// and rename flows in app.js use a manual XHR (not htmx) and then call
// htmx.trigger(tbody, 'refresh') — but #ports-body's hx-trigger is
// "load, every 2s" with no "refresh" event, so that trigger is a
// no-op. Without this helper the test assertion on post-mutation DOM
// can wait up to the next 2s poll tick and miss a tight timeout budget.
// Waits for the /ports 200 response so the caller can assert against a
// deterministic post-swap DOM rather than racing htmx's internal timer.
export async function forcePortsRefresh(page: Page, timeout = 8000): Promise<void> {
  const resp = page.waitForResponse(
    (r) => r.url().endsWith('/ports') && r.status() === 200,
    { timeout },
  );
  await page.evaluate(() => {
    const h = (window as unknown as { htmx?: { ajax: (m: string, u: string, o: unknown) => unknown } }).htmx;
    if (!h) return;
    h.ajax('GET', '/ports', { target: '#ports-body', swap: 'innerHTML' });
  });
  await resp;
}

// waitForPort polls /ports.json directly until the listener on `port` is
// observed alive by the dashboard's scanner, then forces an htmx /ports
// fetch so the DOM reflects the new state, then returns a visible-row
// locator. This eliminates the race between the htmx 2s poll cycle, the
// 1s scan budget on the server, and the network swap into #ports-body.
// The JSON endpoint is the same data source the htmx-driven HTML
// fragment renders from, so once the API confirms the row, forcing an
// immediate swap produces visible markup synchronously with the test.
export async function waitForPort(
  page: Page,
  port: number,
  timeout = 8000,
): Promise<Locator> {
  await expect
    .poll(
      async () => {
        const res = await page.request.get('/ports.json', {
          headers: { Authorization: `Bearer ${E2E_CONFIG.AUTH_TOKEN}` },
        });
        if (res.status() !== 200) return false;
        const rows = await res.json();
        return Array.isArray(rows) && rows.some((r: { port: number; alive: boolean }) => r.port === port && r.alive);
      },
      { timeout, intervals: [100, 200, 300, 500, 750] },
    )
    .toBeTruthy();
  await forcePortsRefresh(page);
  return page.locator(`[data-port="${port}"]:visible`).first();
}

// waitForPortGone is the inverse of waitForPort — polls /ports.json until
// the listener on `port` is no longer observed alive, then forces an
// htmx /ports refresh so the row is actually removed from the DOM. Use
// after a kill to assert both API-level disappearance AND DOM removal.
// On non-200 status or a non-array body the poll returns false (treats
// the state as "still present / unknown") so transient server errors
// don't short-circuit the wait and race the subsequent DOM assertion.
export async function waitForPortGone(
  page: Page,
  port: number,
  timeout = 8000,
): Promise<void> {
  await expect
    .poll(
      async () => {
        const res = await page.request.get('/ports.json', {
          headers: { Authorization: `Bearer ${E2E_CONFIG.AUTH_TOKEN}` },
        });
        if (res.status() !== 200) return false;
        const rows = await res.json();
        if (!Array.isArray(rows)) return false;
        return !rows.some((r: { port: number; alive: boolean }) => r.port === port && r.alive);
      },
      { timeout, intervals: [100, 200, 300, 500, 750] },
    )
    .toBeTruthy();
  await forcePortsRefresh(page);
}
