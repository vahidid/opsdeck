import { defineConfig } from "@playwright/test";

// The README screenshots (docs/assets/screenshot-*.png) from the demo
// workspace of tools/e2e (-demo):
//
//   OPSDECK_E2E_ARGS="-demo -postgres shop:pw@127.0.0.1:5432/shop" \
//   OPSDECK_DEMO_SRV=/srv/shop npx playwright test -c screenshots.config.ts
//
// Without -postgres the Databases shots are skipped.
export default defineConfig({
  testDir: "screenshots",
  globalSetup: "./setup.ts",
  workers: 1,
  timeout: 90_000,
  expect: { timeout: 15_000 },
  reporter: "list",
  use: {
    viewport: { width: 1440, height: 810 },
    deviceScaleFactor: 2400 / 1440,
    colorScheme: "dark",
  },
});
