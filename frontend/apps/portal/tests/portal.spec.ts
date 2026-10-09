import { expect, test } from "@playwright/test";
import { z } from "zod";

const reportingPage = z.object({
  ok: z.literal(true),
  data: z.object({ rows: z.array(z.object({ id: z.string(), name: z.string() })), next_cursor: z.string().nullable() }),
});

test("operator can sign in, open a table, and sign out", async ({ page }) => {
  const password = process.env.DDP_TEST_PASSWORD;
  if (!password) throw new Error("DDP_TEST_PASSWORD is required");
  await page.goto("/customers");
  await expect(page.getByRole("heading", { name: "Your operation, in one clear view." })).toBeVisible();
  await page.getByLabel("Email").fill(process.env.DDP_TEST_EMAIL ?? "operator@example.test");
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();

  const tableLink = page.locator("nav[aria-label='Workspace navigation'] a").first();
  await expect(tableLink).toBeVisible();
  const tableLabel = await tableLink.textContent();
  await tableLink.click();
  await expect(page.getByRole("heading", { name: tableLabel ?? "" })).toBeVisible();
  await expect(page.getByRole("table", { name: tableLabel ?? "" })).toBeVisible();
  await expect(page.getByRole("cell", { name: "Synthetic Customer" })).toBeVisible();
  await expect(page.getByRole("cell", { name: "Example Customer" })).toBeVisible();

  const first = reportingPage.parse(await (await page.request.get("/api/customers?limit=1")).json());
  expect(first.data.rows).toHaveLength(1);
  expect(first.data.next_cursor).not.toBeNull();
  const second = reportingPage.parse(await (await page.request.get(`/api/customers?limit=1&cursor=${encodeURIComponent(first.data.next_cursor ?? "")}`)).json());
  expect(second.data.rows).toHaveLength(1);
  expect(second.data.rows[0]?.id).not.toBe(first.data.rows[0]?.id);
  expect(second.data.next_cursor).toBeNull();
  const searched = reportingPage.parse(await (await page.request.get("/api/customers?q=sYnThEtIc")).json());
  expect(searched.data.rows.map((row) => row.name)).toEqual(["Synthetic Customer"]);
  const exported = await page.request.get("/api/customers/export.csv");
  expect(exported.ok()).toBe(true);
  expect(exported.headers()["content-type"]).toContain("text/csv");
  expect(await exported.text()).toContain("Synthetic Customer");

  await page.locator(".account-menu summary").click();
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
  expect((await page.request.get("/api/customers/export.csv")).status()).toBe(401);
});
