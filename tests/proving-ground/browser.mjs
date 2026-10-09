// Run from the frontend workspace to use its installed Playwright dependency.
import { readFileSync } from 'node:fs';
import { chromium, expect } from '@playwright/test';

const { execution } = JSON.parse(readFileSync(process.env.DDP_OPERATION_EVIDENCE, 'utf8'));
const browser = await chromium.launch();
try {
  const page = await browser.newPage({ baseURL: process.env.DDP_BASE_URL });
  const path = `/api/system/runs/${execution.id}/logs`;
  expect((await page.request.get(path)).status()).toBe(401);
  await page.goto('/system');
  await page.getByLabel('Email', { exact: true }).fill('operator@example.test');
  await page.getByLabel('Password', { exact: true }).fill('synthetic-browser-password-2026');
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await page.getByRole('navigation').getByRole('link', { name: 'System', exact: true }).click();
  const region = page.getByRole('region', { name: 'Recent failures', exact: true });
  const failure = region.locator('.console-row').filter({ hasText: execution.id });
  await expect(failure).toContainText('job/sync_customers');
  await expect(failure).toContainText('failed');
  await failure.getByRole('button', { name: 'View logs', exact: true }).click();
  await expect(page.getByRole('heading', { name: `Logs · job/sync_customers · ${execution.id}` })).toBeFocused();
  await expect(page.getByText(execution.last_attempt.error, { exact: true }).last()).toBeVisible();
  const logs = await (await page.request.get(path)).json();
  expect(logs.ok).toBe(true);
  expect(logs.data).toHaveLength(2);
  const last = logs.data.find(log => log.number === 2);
  expect(last.execution_id).toBe(execution.id);
  expect(last.error).toBe(execution.last_attempt.error);
  expect(last.stderr).toBe(execution.last_attempt.stderr);
  expect(last.stdout).toBe(execution.last_attempt.stdout);
  console.log(`Console inspected the same failed execution ${execution.id}`);
} finally {
  await browser.close();
}
