import { expect, test } from "@playwright/test";
import { open } from "./fixture";

test("connect an outside agent: the key is shown once and can be revoked", async ({ page }) => {
  await open(page, "/settings");
  await page.getByRole("button", { name: "Connect an agent" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("combobox", { name: /^Agent/ }).selectOption("Claude Code");
  await dialog.getByRole("textbox", { name: /^Label/ }).fill("Claude Code in CI");
  await dialog.getByRole("button", { name: "Make the key" }).click();

  const setup = page.getByRole("dialog", { name: /Set up Claude Code in CI/ });
  await expect(setup.getByText("This is the only time the key is shown")).toBeVisible();
  await expect(setup.getByText(/^odk_/).first()).toBeVisible();
  await expect(setup.getByText(/claude mcp add --transport http opsdeck-acme/)).toBeVisible();
  await setup.getByRole("button", { name: "Done" }).click();

  await expect(page.getByText("Claude Code in CI")).toBeVisible();
  await expect(page.getByText("not used yet")).toBeVisible();
  await page.getByRole("button", { name: "Revoke" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Revoke" }).click();
  await expect(page.getByText("Claude Code in CI")).toBeHidden();
});
