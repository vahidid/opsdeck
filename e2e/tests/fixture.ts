import { expect, type Page } from "@playwright/test";

export const server = JSON.parse(process.env.OPSDECK_E2E ?? "{}") as { url: string; base: string; password: string; hostDir: string };

// open logs in with the token link and unlocks the vault if it is locked,
// then goes to path.
export async function open(page: Page, path = "/w/acme/overview") {
  await page.goto(server.url);
  const pw = page.getByPlaceholder("Master password");
  const shell = page.getByRole("button", { name: "Lock vault" });
  await expect(pw.or(shell)).toBeVisible();
  if (await pw.isVisible()) {
    await pw.fill(server.password);
    await pw.press("Enter");
    await expect(shell).toBeVisible();
  }
  if (path) {
    await page.goto(server.base + path);
    await expect(shell).toBeVisible();
  }
}
