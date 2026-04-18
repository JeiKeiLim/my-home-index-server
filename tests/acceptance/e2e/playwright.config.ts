import { defineConfig, devices } from '@playwright/test';

const E2E_AUTH_TOKEN = 'e2etest-0123456789abcdef';
const E2E_SESSION_SECRET = 'e2e-secret-0123456789abcdef0123';
const DASHBOARD_PORT = 40091;

export default defineConfig({
  testDir: './specs',
  timeout: 30_000,
  fullyParallel: false, // we reuse a single backend + shared port range
  retries: 0,
  reporter: [['list'], ['html', { open: 'never' }]],
  use: {
    baseURL: `http://localhost:${DASHBOARD_PORT}`,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'desktop-chromium',
      use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 } },
    },
    {
      name: 'mobile-safari',
      use: { ...devices['iPhone 14'] }, // 390x844
    },
  ],
  globalSetup: './global-setup.ts',
  globalTeardown: './global-teardown.ts',
  webServer: {
    command: `../../../port-manager --port ${DASHBOARD_PORT} --public-host localhost --trust-xff`,
    cwd: __dirname,
    port: DASHBOARD_PORT,
    timeout: 10_000,
    env: {
      AUTH_TOKEN: E2E_AUTH_TOKEN,
      SESSION_SECRET: E2E_SESSION_SECRET,
      PUBLIC_HOST: 'localhost',
      PORT_RANGE: '40190-40199', // e2e reserves this sub-range for seeded listeners
      KILL_GRACE_MS: '500', // fast-fail tests
      // TRUST_XFF lets each spec/test rotate X-Forwarded-For so that
      // S8 (rate-limit lockout) intentionally exhausts only its own
      // bucket and does not cascade-fail S1/S2/S3/S4/S6. SAFE here
      // because the binary listens on localhost only behind the
      // Playwright fixture; production deployments must keep this
      // OFF unless a trusted reverse proxy rewrites XFF.
      TRUST_XFF: 'true',
    },
    reuseExistingServer: !process.env.CI,
  },
});

export const E2E_CONFIG = {
  AUTH_TOKEN: E2E_AUTH_TOKEN,
  DASHBOARD_PORT,
  PORT_RANGE_MIN: 40190,
  PORT_RANGE_MAX: 40199,
};
