// Driven by the Go integration test against a disposable Postgres and real API.
import { chromium, expect } from '@playwright/test';

const base = process.env.DDP_BASE_URL;
const browser = await chromium.launch();
try {
  const page = await browser.newPage({ baseURL: base });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(`/welcome#token=${process.env.DDP_WELCOME_TOKEN}`);
  await expect(page.getByRole('heading', { name: 'Set password', exact: true })).toBeVisible();
  await expect.poll(() => new URL(page.url()).hash).toBe('');
  await page.screenshot({ path: '/tmp/ddp-account-form.png' });
  await page.getByLabel('New password', { exact: true }).fill('browser-password');
  await page.getByLabel('Confirm new password').fill('different-password');
  await page.getByRole('button', { name: 'Set password' }).click();
  await expect(page.getByText('Passwords must match.')).toBeVisible();
  await page.getByLabel('Confirm new password').fill('browser-password');
  await page.getByRole('button', { name: 'Set password' }).click();
  await expect(page.getByRole('status')).toContainText('Password updated.');
  expect((await page.request.get('/api/auth/session')).status()).toBe(401);
  await page.getByRole('link', { name: 'Sign in normally' }).click();
  await page.getByLabel('Email', { exact: true }).fill('browser@example.test');
  await page.getByLabel('Password', { exact: true }).fill('browser-password');
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Workspace', exact: true })).toBeVisible();

  // Existing login must not hide the recovery form or bypass token validation.
  await page.goto('/forgot-password');
  await page.getByLabel('Email', { exact: true }).fill('user@example.test');
  await page.getByRole('button', { name: 'Send reset link' }).click();
  await expect(page.getByRole('status')).toContainText('If an account uses that email');
  await page.goto(`/reset-password#token=${process.env.DDP_RESET_TOKEN}`);
  await expect.poll(() => new URL(page.url()).hash).toBe('');
  await page.getByLabel('New password', { exact: true }).fill('browser-reset-password');
  await page.getByLabel('Confirm new password').fill('browser-reset-password');
  await page.getByRole('button', { name: 'Set password' }).click();
  await expect(page.getByRole('status')).toContainText('Password updated.');
  expect((await page.request.get('/api/auth/session')).status()).toBe(401);
  await page.getByRole('link', { name: 'Sign in normally' }).click();
  await page.getByLabel('Email', { exact: true }).fill('user@example.test');
  await page.getByLabel('Password', { exact: true }).fill('browser-reset-password');
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Workspace', exact: true })).toBeVisible();
  expect(errors).toEqual([]);
} finally {
  await browser.close();
}
