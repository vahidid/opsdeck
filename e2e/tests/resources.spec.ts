import { expect, test } from "@playwright/test";
import { open } from "./fixture";

test("add a resource with the product picker", async ({ page }) => {
  const name = `orders-db-${Date.now() % 100000}`;
  await open(page, "/w/acme/resources");
  await page.getByRole("button", { name: "Add resource" }).first().click();

  // search by product, choose with the keyboard
  const search = page.getByLabel("Search resource types");
  await expect(search).toBeFocused();
  await search.fill("postgres");
  await search.press("Enter");

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("saved as Database with engine: postgres")).toBeVisible();
  await expect(dialog.getByText("Required: Address")).toBeVisible();
  await dialog.getByRole("textbox", { name: /^Name/ }).fill(name);
  await dialog.getByRole("textbox", { name: /^Address/ }).fill("10.20.2.9:5432");
  await dialog.getByRole("button", { name: "prod", exact: true }).click();
  await dialog.getByRole("combobox", { name: /^Reached via/ }).selectOption("host/bastion");
  await dialog.getByRole("button", { name: "Add PostgreSQL" }).click();
  await expect(dialog).toBeHidden();

  const row = page.getByRole("row", { name: new RegExp(`^${name}`) });
  await expect(row).toBeVisible();
  await expect(row).toContainText("prod");
  await expect(row).toContainText("host/bastion");

  // it is a database now
  await page.goto(page.url().replace(/\/resources.*/, "/databases"));
  // (with several databases it is one of the options of the picker)
  await expect(page.getByRole("main").getByText(name).first()).toBeAttached();
});

test("the picker finds nothing for nonsense and offers the agent", async ({ page }) => {
  await open(page, "/w/acme/resources");
  await page.getByRole("button", { name: "Add resource" }).first().click();
  await page.getByLabel("Search resource types").fill("zzzz-no-such-thing");
  await expect(page.getByText("Nothing matches")).toBeVisible();
  await page.getByRole("button", { name: "Let the agent set it up" }).click();
  await expect(page).toHaveURL(/\/w\/acme\/agent/);
});
