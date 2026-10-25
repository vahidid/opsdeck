import { expect, test } from "@playwright/test";
import { open } from "./fixture";

test("the command palette jumps to a section and to a host", async ({ page }) => {
  await open(page);
  await page.keyboard.press("Control+k");
  const input = page.getByPlaceholder("Search sections, hosts, resources, actions…");
  await expect(input).toBeFocused();
  await input.fill("docker");
  await input.press("Enter");
  await expect(page).toHaveURL(/\/w\/acme\/docker/);

  await page.keyboard.press("Control+k");
  await input.fill("app-1");
  await expect(page.getByRole("dialog").getByText("app-1").first()).toBeVisible();
  await input.press("Enter");
  await expect(page).not.toHaveURL(/\/docker$/);
});

test("customize: accent and density stay after a reload", async ({ page }) => {
  await open(page);
  await page.getByRole("button", { name: "Customize" }).click();
  const panel = page.getByRole("dialog");
  await panel.getByRole("button", { name: "Blue" }).click();
  await panel.getByRole("button", { name: "Compact" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-accent", "blue");
  await expect(page.locator("html")).toHaveAttribute("data-density", "compact");
  await panel.getByRole("button", { name: "Done" }).click();

  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-accent", "blue");
  await expect(page.locator("html")).toHaveAttribute("data-density", "compact");

  await page.getByRole("button", { name: "Customize" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Reset to defaults" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-accent", "workspace");
});

test("the layout fits a phone", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  const page = await ctx.newPage();
  await open(page, "/w/acme/resources");
  await expect(page.getByRole("row", { name: /^bastion/ })).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  await ctx.close();
});
