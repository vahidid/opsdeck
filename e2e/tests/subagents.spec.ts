import { expect, test } from "@playwright/test";
import { open } from "./fixture";

// "e2e-delegate" makes the scripted model hand a read-only check to the
// investigator subagent, which reads the inventory and reports. Every model
// response uses 100 tokens in and 20 out; the model's window is 128k.
test("a subagent run shows under its delegate call with its status, numbers and report", async ({ page }) => {
  await open(page, "/w/acme/agent?new=1");
  const composer = page.getByPlaceholder(/Ask the agent to investigate or change something/);
  await composer.fill("e2e-delegate: is app-1 up?");
  await composer.press("Enter");
  await expect(page.getByText("E2E-SUB-DONE")).toBeVisible({ timeout: 30_000 });

  // every call open: the delegate call shows the subagent's run inside it
  await page.getByRole("button", { name: "Detailed" }).click();
  await expect(page.getByTestId("subagent-status")).toHaveText("done");
  await expect(page.getByText("read-only", { exact: true })).toBeVisible();
  await expect(page.getByTestId("subagent-usage")).toHaveText("· 2 steps · 1 tool calls · 200 in / 40 out");
  // its own context next to the session's, both against the reported window
  const meters = page.getByTestId("context-meter");
  await expect(meters).toHaveCount(2);
  await expect(meters.nth(1)).toHaveText("context 120 / 128k · 0%");
  await expect(page.getByText("app-1 is up (SUB-REPORT).")).toBeVisible();
  // the session's totals include the subagent's tokens
  await expect(page.getByTestId("session-tokens")).toHaveText("400 in · 80 out");

  await page.getByRole("button", { name: "Task" }).click();
  await expect(page.getByText("Check whether app-1 is up and report.").last()).toBeVisible();
});
