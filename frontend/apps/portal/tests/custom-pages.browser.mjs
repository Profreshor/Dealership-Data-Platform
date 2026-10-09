import { chromium, expect } from '@playwright/test';

const browser = await chromium.launch();
const baseURL = process.env.DDP_BASE_URL;
try {
  async function login(email, password, path = '/custom-report') {
    const page = await browser.newPage({ baseURL });
    await page.goto(path);
    await page.getByLabel('Email', { exact: true }).fill(email);
    await page.getByLabel('Password', { exact: true }).fill(password);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    return page;
  }
  const anonymous = await browser.newPage({ baseURL });
  await anonymous.goto('/custom-report');
  await expect(anonymous.getByRole('button', { name: 'Sign in', exact: true })).toBeVisible();
  expect((await anonymous.request.get('/api/custom-proof')).status()).toBe(401);

  const member = await login('user@example.test', 'user-password');
  await expect(member.getByRole('heading', { name: 'Page unavailable' })).toBeVisible();
  await expect(member.getByRole('link', { name: 'Client report', exact: true })).toHaveCount(0);
  expect((await member.request.get('/api/custom-proof')).status()).toBe(403);

  const manager = await login('manager@example.test', 'manager-password');
  await expect(manager.getByRole('heading', { name: 'Synthetic observation' })).toBeVisible();
  const observation = manager.getByRole('status');
  await expect(observation).toHaveText(/Observation \d+/);
  const before = await observation.textContent();
  await manager.getByRole('button', { name: 'Refresh observation' }).click();
  await expect(observation).not.toHaveText(before ?? '');
  await manager.setViewportSize({ width: 1365, height: 900 });
  await manager.screenshot({ path: '/tmp/ddp-custom-desktop.png' });
  await manager.setViewportSize({ width: 390, height: 844 });
  await manager.screenshot({ path: '/tmp/ddp-custom-mobile.png' });
  expect(await manager.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await manager.goto('/');
  await expect(manager.getByRole('heading', { name: 'Synthetic workspace' })).toBeVisible();
  await expect(manager.getByText('This page is not ready yet.')).toBeVisible();
  await manager.goto('/operator-note');
  await expect(manager.getByRole('heading', { name: 'Page unavailable' })).toBeVisible();

  const admin = await login('admin@example.test', 'admin-password', '/operator-note');
  await expect(admin.getByRole('heading', { name: 'Operator note' })).toBeVisible();
  await admin.goto('/retry-example');
  await expect(admin.getByRole('alert')).toContainText('Page failed to load.');
  await expect(admin.getByRole('navigation', { name: 'Workspace navigation' })).toBeVisible();
  await expect(admin.getByText('Synthetic component failure', { exact: true })).toHaveCount(0);
  await admin.evaluate(() => sessionStorage.setItem('ddp_retry_ready', '1'));
  await admin.getByRole('button', { name: 'Try again', exact: true }).click();
  await expect(admin.getByRole('heading', { name: 'Recovered client page' })).toBeVisible();
} finally {
  await browser.close();
}
