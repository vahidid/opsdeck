import { expect, test } from "@playwright/test";
import { open } from "./fixture";

// A hook added in the workspace settings is trusted and shows its last
// result once the agent's calls ran it.
test("the hooks editor shows each hook's trust state and last result", async ({ page }) => {
  await open(page, "/w/acme/settings");
  await page.getByRole("button", { name: "Add hook" }).click();
  await page.getByPlaceholder("name").last().fill("e2e-audit");
  await page.getByPlaceholder("~/bin/opsdeck-policy").last().fill("cat >/dev/null");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("button", { name: "Save changes" })).toBeDisabled();
  const lastRun = page.getByTestId("hook-last-run").last();
  await expect(page.getByText("trusted", { exact: true }).last()).toBeVisible();
  await expect(lastRun).toHaveText("Not run since opsdeck started.");

  // the scripted agent reads the inventory first: the before_tool hook runs
  await open(page, "/w/acme/agent?new=1");
  const composer = page.getByPlaceholder(/Ask the agent to investigate or change something/);
  await composer.fill("check app-1 and write the marker file");
  await composer.press("Enter");
  await expect(page.getByRole("button", { name: "Approve", exact: true })).toBeVisible({ timeout: 30_000 });
  await page.getByRole("button", { name: "Deny…" }).click();
  await page.getByRole("button", { name: "Deny", exact: true }).click();
  await expect(page.getByText("E2E-DONE")).toBeVisible({ timeout: 30_000 });

  await open(page, "/w/acme/settings");
  await expect(lastRun).toContainText("Last run");
  await expect(lastRun).toContainText("ran");
  await expect(lastRun).toContainText("acme");

  // leave the workspace as it was for the other tests
  await page.getByRole("button", { name: "Remove hook" }).last().click();
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("button", { name: "Save changes" })).toBeDisabled();
});
