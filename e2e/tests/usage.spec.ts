import { expect, test } from "@playwright/test";
import { open } from "./fixture";

// The scripted model's server reports a context window of 128k for its model
// (GET /models, like OpenRouter) and rate limit headers with every response;
// each response uses 100 tokens in and 20 out.
test("the chat shows context, tokens and the provider's reported limits", async ({ page }) => {
  await open(page, "/w/acme/agent?new=1");
  const composer = page.getByPlaceholder(/Ask the agent to investigate or change something/);
  await composer.fill("check app-1 and write the marker file");
  await composer.press("Enter");

  // during the turn: the first response's usage, against the reported window
  const meter = page.getByTestId("context-meter");
  await expect(meter).toHaveText("context 120 / 128k · 0%", { timeout: 30_000 });
  await expect(page.getByTestId("session-tokens")).toContainText("out");
  // the turn stops at the approval of the change on prod, after two responses
  await expect(page.getByRole("button", { name: "Approve", exact: true })).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId("session-tokens")).toHaveText("200 in · 40 out");

  await page.getByRole("button", { name: "Limits" }).click();
  const demo = page.getByTestId("provider-limits-fake");
  await expect(demo).toContainText("this session");
  await expect(demo).toContainText("requests");
  await expect(demo).toContainText("59 / 60 left");

  // leave nothing waiting for the other tests
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Deny…" }).click();
  await page.getByRole("button", { name: "Deny", exact: true }).click();
  await expect(page.getByText("E2E-DONE")).toBeVisible({ timeout: 30_000 });
});
