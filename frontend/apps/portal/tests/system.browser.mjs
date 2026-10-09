// Real API and disposable Postgres fixture owned by system_browser_test.go.
import { chromium, expect } from '@playwright/test';

const browser = await chromium.launch();
try {
  const context = await browser.newContext({ baseURL: process.env.DDP_BASE_URL });
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const routes = ['/api/system/status', '/api/system/runs', `/api/system/runs/${process.env.DDP_SYSTEM_RUN}/logs`];
  for (const path of routes) expect((await page.request.get(path)).status()).toBe(401);
  async function login(email, password, path = '/') {
    await page.goto(path);
    await page.getByLabel('Email', { exact: true }).fill(email);
    await page.getByLabel('Password', { exact: true }).fill(password);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  }
  await login('admin@example.test', 'admin-password');
  await page.getByRole('navigation').getByRole('link', { name: 'System', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'System', exact: true })).toBeVisible();
  await expect(page.getByText('Synthetic scheduler heartbeat missing')).toBeVisible();
  await expect(page.getByText('Saved health evidence is stale or incomplete.')).toBeVisible();
  for (const path of routes) {
    const response = await page.request.get(path);
    expect(response.status()).toBe(200);
    expect(response.headers()['cache-control']).toBe('no-store');
  }
  expect((await (await page.request.get(routes[0])).json()).data.health_observation).toBe('stale');
  await page.getByRole('button', { name: 'View logs', exact: true }).first().click();
  await expect(page.getByText('<script>console fixture</script>', { exact: true })).toBeVisible();
  await expect(page.getByText('Synthetic upstream unavailable', { exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: `Logs · job/sync_customers · ${process.env.DDP_SYSTEM_RUN}` })).toBeFocused();
  await page.screenshot({ path: '/tmp/ddp-system-desktop.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: '/tmp/ddp-system-mobile.png', fullPage: true });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

  await page.getByRole('region', { name: 'Recent runs', exact: true }).getByRole('button', { name: 'View logs', exact: true }).last().click();
  await expect(page.getByText('No attempts recorded.', { exact: true })).toBeVisible();
  await expect(page.getByText('Synthetic upstream unavailable', { exact: true })).toHaveCount(0);
  await page.route('**/api/system/status', route => route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ ok: false, data: null, error: { code: 'query_failed', message: 'System query failed' } }) }), { times: 1 });
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Couldn’t load system status.');
  await page.getByRole('button', { name: 'Try again', exact: true }).click();
  await expect(page.getByText('Synthetic scheduler heartbeat missing')).toBeVisible();

  // A client manager can view their business table, but cannot inspect the system.
  await context.clearCookies();
  await login('manager@example.test', 'manager-password');
  await expect(page.getByRole('heading', { name: 'Workspace', exact: true })).toBeVisible();
  await expect(page.getByRole('navigation').getByRole('link', { name: 'System', exact: true })).toHaveCount(0);
  await page.getByRole('navigation').getByRole('link', { name: 'Customers', exact: true }).click();
  await expect(page.getByRole('cell', { name: 'Console Customer', exact: true })).toBeVisible();
  await page.goto('/system');
  await expect(page.getByRole('heading', { name: 'Page unavailable', exact: true })).toBeVisible();
  for (const path of routes) expect((await page.request.get(path)).status()).toBe(403);
  expect(errors).toEqual([]);
} finally {
  await browser.close();
}
