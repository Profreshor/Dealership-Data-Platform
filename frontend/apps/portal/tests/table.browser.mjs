import { chromium, expect } from '@playwright/test';
import { readFile } from 'node:fs/promises';

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ baseURL: process.env.DDP_BASE_URL });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto('/customers');
  await page.getByLabel('Email', { exact: true }).fill('manager@example.test');
  await page.getByLabel('Password', { exact: true }).fill('manager-password');
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  const table = page.getByRole('table', { name: 'Customers', exact: true });
  const names = () => table.locator('tbody tr td:last-child');
  const apply = () => page.getByRole('button', { name: 'Apply', exact: true }).click();
  const reset = () => page.getByRole('button', { name: 'Reset', exact: true }).click();
  await expect(names()).toHaveText(['—', 'Alpha Customer']);
  await expect(page.getByRole('button', { name: 'Previous', exact: true })).toBeDisabled();
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await expect(names()).toHaveText(['Beta Customer', 'Gamma Customer']);
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await expect(names()).toHaveText(['Quote & %_ Customer', 'Zulu Customer']);
  await expect(page.getByRole('button', { name: 'Next', exact: true })).toBeDisabled();
  await page.getByRole('button', { name: 'Previous', exact: true }).click();
  await expect(names()).toHaveText(['Beta Customer', 'Gamma Customer']);

  // Applying criteria while on page two starts a new keyset at page one.
  await page.getByLabel('Search', { exact: true }).fill('%_');
  await apply();
  await expect(names()).toHaveText(['Quote & %_ Customer']);
  await expect(page.getByText('Page 1', { exact: true })).toBeVisible();
  await reset();
  await expect(names()).toHaveText(['—', 'Alpha Customer']);
  await page.getByRole('checkbox', { name: 'Filter name', exact: true }).check();
  await page.getByLabel('name exact value', { exact: true }).fill('Beta Customer');
  await apply();
  await expect(names()).toHaveText(['Beta Customer']);
  await page.getByLabel('name exact value', { exact: true }).fill('');
  await apply();
  await expect(names()).toHaveText(['—']);
  await page.getByLabel('name exact value', { exact: true }).fill('Not a customer');
  await apply();
  await expect(page.getByText('No matching rows', { exact: true })).toBeVisible();
  await reset();
  await page.getByLabel('Sort', { exact: true }).selectOption({ label: 'name descending' });
  await apply();
  await expect(names()).toHaveText(['Zulu Customer', 'Quote & %_ Customer']);
  await expect(page.getByText('Export includes the current filters and sort, up to 3 rows.', { exact: true })).toBeVisible();
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({ path: '/tmp/ddp-table-desktop.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({ path: '/tmp/ddp-table-mobile.png', fullPage: true });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

  // Export ignores the displayed cursor and page size, retaining applied criteria.
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await expect(names()).toHaveText(['Gamma Customer', 'Beta Customer']);
  const exportRequest = page.waitForRequest(request => request.url().includes('/export.csv'));
  const downloaded = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Download CSV', exact: true }).click();
  const request = await exportRequest;
  const query = new URL(request.url()).searchParams;
  expect(query.has('cursor')).toBe(false);
  expect(query.has('limit')).toBe(false);
  const download = await downloaded;
  expect(await download.failure()).toBeNull();
  expect(await readFile(await download.path(), 'utf8')).toBe('id,name\n5,Zulu Customer\n4,Quote & %_ Customer\n3,Gamma Customer\n');
  await page.route('**/api/customers/export.csv*', route => route.fulfill({ status: 403, contentType: 'application/json', body: JSON.stringify({ ok: false, data: null, error: { code: 'forbidden', message: 'Access denied' } }) }), { times: 1 });
  await page.getByRole('button', { name: 'Download CSV', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Download failed: Access denied');

  // A failed table query must not keep showing cached rows as current data.
  await page.route('**/api/customers?*', route => route.fulfill({ status: 403, contentType: 'application/json', body: JSON.stringify({ ok: false, data: null, error: { code: 'forbidden', message: 'Access denied' } }) }), { times: 1 });
  await page.getByLabel('Search', { exact: true }).fill('Alpha');
  await apply();
  await expect(page.getByText('Couldn’t load this table.', { exact: true })).toBeVisible();
  await expect(table).toHaveCount(0);
  await page.getByRole('button', { name: 'Try again', exact: true }).click();
  await expect(names()).toHaveText(['Alpha Customer']);

  await page.getByRole('navigation', { name: 'Workspace navigation' }).getByRole('link', { name: 'Summary', exact: true }).click();
  await expect(page.getByRole('table', { name: 'Summary', exact: true }).getByRole('cell', { name: '6', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Next', exact: true })).toHaveCount(0);
  await expect(page.getByLabel('Sort', { exact: true })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Download CSV', exact: true })).toHaveCount(0);
  await page.route('**/api/summary', route => route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ ok: false, data: null, error: { code: 'not_found', message: 'Row not found' } }) }), { times: 1 });
  await page.reload();
  await expect(page.getByText('No rows yet', { exact: true })).toBeVisible();
  await expect(page.getByRole('table', { name: 'Summary', exact: true })).toHaveCount(0);
  expect(errors).toEqual([]);
} finally {
  await browser.close();
}
