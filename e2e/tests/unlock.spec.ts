import { expect, test } from "@playwright/test";
import { open, server } from "./fixture";

test("the token link logs in; a wrong password is refused, the right one unlocks", async ({ page }) => {
  await open(page, "");
  await page.getByRole("button", { name: "Lock vault" }).click();

  const pw = page.getByPlaceholder("Master password");
  await expect(page.getByText("Vault locked")).toBeVisible();
  await pw.fill("not the password");
  await pw.press("Enter");
  await expect(page.getByText("wrong master password")).toBeVisible();
  await expect(pw).toBeVisible();

  await pw.fill(server.password);
  await pw.press("Enter");
  await expect(page).toHaveURL(/\/w\/acme\/overview/);
  await expect(page.getByRole("main").getByText("ACME Shop").first()).toBeVisible();
});

test("without the token the UI asks to open the link from the terminal", async ({ browser }) => {
  const page = await (await browser.newContext()).newPage();
  await page.goto(server.base + "/");
  await expect(page.getByText("Open opsdeck from your terminal")).toBeVisible();
});
