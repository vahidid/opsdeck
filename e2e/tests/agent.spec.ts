import { existsSync, readFileSync, rmSync } from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { open, server } from "./fixture";

const marker = path.join(server.hostDir, "marker.txt");

// ask starts a new session and sends the scripted model a task: it reads the
// inventory and the host, then wants to write a file on app-1 (prod, protected).
async function ask(page: Page) {
  await open(page, "/w/acme/agent?new=1");
  const composer = page.getByPlaceholder(/Ask the agent to investigate or change something/);
  await composer.fill("check app-1 and write the marker file");
  await composer.press("Enter");
  // the reads run without asking
  await expect(page.getByText("I'll look at the inventory and the app host first.")).toBeVisible();
  const approve = page.getByRole("button", { name: "Approve", exact: true });
  await expect(approve).toBeVisible({ timeout: 30_000 });
  await expect(page.locator("[data-pending-approval]")).toContainText("echo deployed > marker.txt");
  await expect(page.getByRole("button", { name: /waiting for you/ })).toBeVisible();
  return approve;
}

test.beforeEach(() => rmSync(marker, { force: true }));

test("the agent waits for approval on prod, then runs the change and reports", async ({ page }) => {
  const approve = await ask(page);
  expect(existsSync(marker)).toBe(false);
  await approve.click();
  await expect(page.getByText("E2E-DONE")).toBeVisible({ timeout: 30_000 });
  await expect(page.getByRole("heading", { name: "Report" })).toBeVisible();
  expect(readFileSync(marker, "utf8").trim()).toBe("deployed");
});

test("a denied change does not run", async ({ page }) => {
  await ask(page);
  await page.getByRole("button", { name: "Deny…" }).click();
  await page.getByRole("button", { name: "Deny", exact: true }).click();
  await expect(page.getByText("Approved", { exact: true })).toBeHidden();
  await expect(page.getByRole("button", { name: "Approve", exact: true })).toBeHidden();
  // the model gets the denial and finishes its script
  await expect(page.getByText("E2E-DONE")).toBeVisible({ timeout: 30_000 });
  expect(existsSync(marker)).toBe(false);
});
