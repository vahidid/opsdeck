import { expect, test } from "@playwright/test";
import { open } from "./fixture";

// The scripted model's change on prod waits for approval, so the turn runs
// while we send more: one message queued for after the turn, one steering the
// turn, one cancelled. Approving lets the turn go on.
test("messages sent while the agent works wait, steer the turn, or are cancelled", async ({ page }) => {
  await open(page, "/w/acme/agent?new=1");
  const composer = page.getByPlaceholder(/Ask the agent to investigate or change something/);
  await composer.fill("check app-1 and write the marker file");
  await composer.press("Enter");
  const approve = page.getByRole("button", { name: "Approve", exact: true });
  await expect(approve).toBeVisible({ timeout: 30_000 });

  const box = page.getByRole("textbox").last();
  const pending = page.getByTestId("pending-message");
  const sendWhileWorking = async (text: string, key: string, n: number) => {
    await box.fill(text);
    await box.press(key);
    await expect(pending).toHaveCount(n);
  };
  await sendWhileWorking("then check the disk", "Enter", 1);
  await sendWhileWorking("forget this one", "Enter", 2);
  await sendWhileWorking("use port 8080", "Control+Enter", 3);
  await expect(pending.nth(0)).toContainText("queued · after this turn");
  await expect(pending.nth(2)).toContainText("steer · next step");
  await pending.nth(1).getByRole("button", { name: "Cancel message" }).click();
  await expect(pending).toHaveCount(2);
  // steering answers no approval: the card still waits
  await expect(approve).toBeVisible();

  await approve.click();
  // the steering message joins this turn after the approved step...
  await expect(page.getByText("steered into the turn")).toBeVisible({ timeout: 30_000 });
  // ...and the queued one starts the next turn when this one ends
  await expect(page.getByText("sent after the turn")).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId("pending-messages")).toBeHidden();
  await expect(page.getByText("forget this one")).toBeHidden();
});
