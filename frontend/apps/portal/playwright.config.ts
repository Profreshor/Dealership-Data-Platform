import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "tests",
  outputDir: "../../test-results",
  use: { baseURL: process.env.DDP_BASE_URL ?? "http://localhost:8080", trace: "retain-on-failure" },
});
