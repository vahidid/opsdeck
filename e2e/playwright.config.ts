import { defineConfig, devices } from "@playwright/test";

// The tests drive a real opsdeck (tools/e2e: a fresh data folder, local SSH
// hosts, a scripted model) through Chromium. They share that one server, so
// they run one after another.
export default defineConfig({
  testDir: "tests",
  globalSetup: "./setup.ts",
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: {
    ...devices["Desktop Chrome"],
    viewport: { width: 1280, height: 800 },
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
});
