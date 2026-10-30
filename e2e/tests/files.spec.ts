import { readFileSync, rmSync } from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { open, server } from "./fixture";

const name = "transfer.txt";
const target = path.join(server.hostDir, name);

test.beforeEach(() => rmSync(target, { force: true }));

test("upload a file to a host and download it again", async ({ page }) => {
  await open(page, "/w/acme/resources");
  await page.getByRole("row", { name: /^app-1/ }).getByRole("button", { name: "More" }).click();
  await page.getByRole("button", { name: "Files" }).click();

  const dialog = page.getByRole("dialog", { name: "Files · app-1" });
  const where = dialog.locator("input:not([type=file])");
  await where.fill(server.hostDir);
  await where.press("Enter");
  await expect(where).toHaveValue(server.hostDir);

  // upload: asks first, then writes the file through SFTP
  await dialog.locator("input[type=file]").setInputFiles({ name, mimeType: "text/plain", buffer: Buffer.from("hello from the browser\n") });
  const confirm = page.getByRole("dialog", { name: `Upload ${name} to app-1?` });
  await confirm.getByRole("button", { name: "Upload" }).click();
  await expect(dialog.getByRole("button", { name: new RegExp(name) })).toBeVisible();
  expect(readFileSync(target, "utf8")).toBe("hello from the browser\n");

  // download from the list
  const row = dialog.getByRole("button", { name: new RegExp(name) });
  await row.hover();
  const download = page.waitForEvent("download");
  await dialog.getByRole("link", { name: `Download ${name}` }).click();
  const file = await download;
  expect(file.suggestedFilename()).toBe(name);
  expect(readFileSync(await file.path(), "utf8")).toBe("hello from the browser\n");

  // a second upload of the same name asks before replacing it
  await dialog.locator("input[type=file]").setInputFiles({ name, mimeType: "text/plain", buffer: Buffer.from("v2\n") });
  await expect(confirm).toContainText("will be replaced");
  await confirm.getByRole("button", { name: "Replace" }).click();
  await expect.poll(() => readFileSync(target, "utf8")).toBe("v2\n");
});
