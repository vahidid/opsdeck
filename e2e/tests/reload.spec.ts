import { expect, test } from "@playwright/test";
import { open } from "./fixture";

// A session reload whose answer arrives after newer live events (slow
// network, busy CI) must not put older state and tokens back in the header.
test("a late session reload does not undo newer live state", async ({ page }) => {
  await open(page, "/w/acme/agent?new=1");
  // from now on, answers to GET /sessions/{id} arrive 2.5 s late
  await page.route(/\/api\/workspaces\/acme\/sessions\/[^/]+$/, async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const res = await route.fetch();
    await new Promise((r) => setTimeout(r, 2500));
    await route.fulfill({ response: res });
  });
  const composer = page.getByPlaceholder(/Ask the agent to investigate or change something/);
  await composer.fill("check app-1 and write the marker file");
  await composer.press("Enter");
  await expect(page.locator("[data-pending-approval]")).toBeVisible({ timeout: 30_000 });
  // wait past the late answers: the header keeps what the stream said
  await page.waitForTimeout(3500);
  await expect(page.getByText("Waiting for your approval").first()).toBeVisible();
  await expect(page.getByTestId("session-tokens")).toHaveText("200 in · 40 out");

  await page.unroute(/\/api\/workspaces\/acme\/sessions\/[^/]+$/);
  await page.getByRole("button", { name: "Deny…" }).click();
  await page.getByRole("button", { name: "Deny", exact: true }).click();
  await expect(page.getByText("E2E-DONE")).toBeVisible({ timeout: 30_000 });
});
