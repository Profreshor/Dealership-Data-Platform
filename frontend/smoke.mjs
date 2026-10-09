import { chromium } from "@playwright/test";

const origin = process.env.DDP_SMOKE_ORIGIN;
const email = process.env.DDP_SMOKE_EMAIL;
const password = process.env.DDP_SMOKE_PASSWORD;
const pagePath = process.env.DDP_SMOKE_PAGE;
if (!origin || !email || !password || !pagePath) throw new Error("smoke browser environment is incomplete");

const browser = await chromium.launch({ headless: true });
process.once("SIGTERM", async () => {
  await browser.close();
  process.exit(143);
});
try {
  const page = await browser.newPage();
  await page.goto(`${origin}/`);
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.locator(".account-menu summary").waitFor();
  const portalResponse = await page.request.get(`${origin}/api/portal`);
  if (!portalResponse.ok()) throw new Error(`portal metadata returned ${portalResponse.status()}`);
  const metadata = (await portalResponse.json()).data;
  const table = metadata?.pages?.find((candidate) => candidate.kind === "table" && candidate.endpoint && candidate.path === pagePath);
  if (!table) throw new Error("portal metadata has no table page");
  await page.goto(`${origin}${table.path}`);
  await page.getByRole("heading", { name: table.label }).waitFor();
  const response = await page.request.get(`${origin}${table.endpoint}`);
  if (!response.ok()) throw new Error(`protected endpoint returned ${response.status()}`);
  const data = await response.json();
  if (!Array.isArray(data.data?.rows) || data.data.rows.length === 0) throw new Error("protected endpoint returned no rows");
  const rendered = page.getByRole("table", { name: table.label });
  await rendered.waitFor();
  const rows = rendered.locator("tbody tr");
  if (await rows.count() !== data.data.rows.length) throw new Error("rendered row count differs from the endpoint");
  const display = (value) => value === null || value === undefined || value === "" ? "—" : typeof value === "boolean" ? value ? "Yes" : "No" : typeof value === "object" ? JSON.stringify(value) : String(value);
  for (let rowIndex = 0; rowIndex < data.data.rows.length; rowIndex += 1) {
    const cells = rows.nth(rowIndex).getByRole("cell");
    if (await cells.count() !== table.columns.length) throw new Error("rendered column count differs from portal metadata");
    for (let columnIndex = 0; columnIndex < table.columns.length; columnIndex += 1) {
      const expected = display(data.data.rows[rowIndex][table.columns[columnIndex]]);
      if (await cells.nth(columnIndex).textContent() !== expected) throw new Error("rendered cell differs from the endpoint");
    }
  }
  await page.locator(".account-menu summary").click();
  await page.getByRole("button", { name: "Sign out" }).click();
  await page.getByRole("button", { name: "Sign in" }).waitFor();
} finally {
  await browser.close();
}
