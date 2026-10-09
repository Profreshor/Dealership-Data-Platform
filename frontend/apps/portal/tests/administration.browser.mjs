// Driven against a disposable database and the embedded production portal.
import { chromium, expect } from '@playwright/test';
const browser = await chromium.launch();
const errors = [];
const login = async (page, email, password, path) => {
  await page.goto(path);
  await page.getByLabel('Email', { exact: true }).fill(email);
  await page.getByLabel('Password', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
};
try {
  const manager = await browser.newPage({ baseURL: process.env.DDP_BASE_URL, viewport: { width: 1440, height: 1000 } });
  const member = await browser.newPage({ baseURL: process.env.DDP_BASE_URL });
  for (const page of [manager, member]) page.on('pageerror', (error) => errors.push(error.message));
  await login(member, 'user@example.test', 'user-password', '/profile');
  await expect(member.getByRole('heading', { name: 'Your profile' })).toBeVisible();
  await member.getByRole('button', { name: 'Send password reset link' }).click();
  await expect(member.getByRole('status')).toContainText('password reset link');
  for (const [name, viewport] of [['desktop', { width: 1440, height: 1000 }], ['mobile', { width: 390, height: 844 }]]) {
    await member.setViewportSize(viewport);
    await member.screenshot({ path: `/tmp/ddp-profile-${name}.png`, fullPage: true });
    expect(await member.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  }
  await member.getByLabel('Account menu').click();
  await expect(member.getByRole('link', { name: 'Users & roles' })).toHaveCount(0);
  await member.goto('/admin/users');
  await expect(member.getByRole('heading', { name: 'Page unavailable' })).toBeVisible();
  expect((await member.request.get('/api/users')).status()).toBe(403);
  await login(manager, 'manager@example.test', 'manager-password', '/admin/users');
  await expect(manager.getByRole('heading', { name: 'Users & roles' })).toBeVisible();
  const operator = manager.getByRole('row').filter({ hasText: 'admin@example.test' });
  await expect(operator.getByRole('button')).toHaveCount(0);
  await expect(operator).toContainText('Read-only');
  expect((await manager.request.get('/api/system/status')).status()).toBe(403);

  await manager.getByLabel('Role to edit').selectOption('__new__');
  // Sequential typing catches accidentally hiding the field after its first character.
  await manager.getByLabel('Stable ID').pressSequentially('report_viewer');
  await expect(manager.getByLabel('Stable ID')).toHaveValue('report_viewer');
  await manager.getByLabel('Name', { exact: true }).fill('Report viewer');
  await manager.getByLabel('customers.read', { exact: true }).check();
  await manager.getByRole('button', { name: 'Save role', exact: true }).click();
  await expect(manager.getByRole('status').filter({ hasText: 'Role saved.' })).toBeVisible();
  const user = manager.getByRole('row').filter({ hasText: 'user@example.test' });
  await user.getByLabel('Roles for user@example.test').selectOption('report_viewer');
  await user.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(manager.getByRole('status').filter({ hasText: 'Account access updated.' })).toBeVisible();
  expect((await member.request.get('/api/auth/session')).status()).toBe(401);
  await login(member, 'user@example.test', 'user-password', '/profile');
  await expect(member.getByText('customers.read', { exact: true })).toBeVisible();

  // An actual rejected write must preserve the displayed account status and offer retry.
  const session = (await (await manager.request.get('/api/auth/session')).json()).data;
  await manager.context().addCookies([{ name: 'ddp_session', value: 'expired-session', url: process.env.DDP_BASE_URL }]);
  await user.getByRole('button', { name: 'Disable', exact: true }).click();
  await expect(manager.getByRole('heading', { name: 'Sign in', exact: true })).toBeVisible();
  await manager.getByLabel('Email', { exact: true }).fill('manager@example.test');
  await manager.getByLabel('Password', { exact: true }).fill('manager-password');
  await manager.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(user).toContainText('Active');
  await user.getByRole('button', { name: 'Disable', exact: true }).click();
  await expect(user).toContainText('Disabled');
  expect((await member.request.get('/api/auth/session')).status()).toBe(401);
  await user.getByRole('button', { name: 'Enable', exact: true }).click();
  await expect(user).toContainText('Active');

  await manager.getByLabel('Email', { exact: true }).fill('invited@example.test');
  await manager.getByLabel('Report viewer', { exact: true }).check();
  await manager.getByRole('button', { name: 'Send invitation' }).click();
  await expect(manager.getByRole('status').filter({ hasText: 'Invitation queued for invited@example.test.' })).toBeVisible();
  await expect(manager.getByRole('row').filter({ hasText: 'invited@example.test' })).toContainText('Pending');
  for (const [name, viewport] of [['desktop', { width: 1440, height: 1000 }], ['mobile', { width: 390, height: 844 }]]) {
    await manager.setViewportSize(viewport);
    await manager.screenshot({ path: `/tmp/ddp-administration-${name}.png`, fullPage: true });
    expect(await manager.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  }
  // Demoting the current manager must invalidate the session and show login.
  await manager.getByLabel('Role to edit').selectOption('manager');
  await manager.getByLabel('users.manage', { exact: true }).uncheck();
  await manager.getByRole('button', { name: 'Save role', exact: true }).click();
  await expect(manager.getByRole('heading', { name: 'Sign in', exact: true })).toBeVisible();
  expect(session.user.admin).toBe(false);
  expect(errors).toEqual([]);
} finally {
  await browser.close();
}
