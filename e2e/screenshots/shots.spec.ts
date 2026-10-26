import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { open, server } from "../tests/fixture";

// One test per README screenshot, in the order they appear there.
const out = process.env.OPSDECK_SHOTS_DIR ?? path.resolve(import.meta.dirname, "../../docs/assets");
const db = (server as { postgres?: string }).postgres;

async function shot(page: Page, name: string) {
  // let charts, fonts and transitions settle; no blinking caret
  await page.addStyleTag({ content: "*{caret-color:transparent!important} *::-webkit-scrollbar{display:none}" });
  await page.waitForTimeout(600);
  await page.screenshot({ path: path.join(out, `screenshot-${name}.png`) });
}


test("overview", async ({ page }) => {
  await open(page, "/w/acme/overview");
  await expect(page.getByText("web-1").first()).toBeVisible();
  await shot(page, "overview");
});

test("agent approval and report", async ({ page }) => {
  await open(page, "/w/acme/agent?new=1");
  const composer = page.getByPlaceholder(/Ask the agent to investigate or change something/);
  await composer.fill("The shop has been returning 502 for about ten minutes. Find out why and fix it.");
  await composer.press("Enter");
  const approve = page.getByRole("button", { name: "Approve", exact: true });
  await expect(approve).toBeVisible({ timeout: 45_000 });
  await page.locator("[data-pending-approval]").scrollIntoViewIfNeeded();
  await shot(page, "agent-approval");
  await approve.click();
  const report = page.getByRole("heading", { name: "The shop is back" });
  await expect(report).toBeVisible({ timeout: 45_000 });
  await page.waitForTimeout(1500);
  await report.scrollIntoViewIfNeeded();
  await shot(page, "agent");
});

test("resources", async ({ page }) => {
  await open(page, "/w/acme/resources");
  await expect(page.getByRole("row", { name: /^web-1/ })).toBeVisible();
  await shot(page, "resources");
});

test("add resource", async ({ page }) => {
  await open(page, "/w/acme/resources");
  await page.getByRole("button", { name: "Add resource" }).first().click();
  await expect(page.getByLabel("Search resource types")).toBeFocused();
  await shot(page, "add-resource");
});

test("docker", async ({ page }) => {
  await open(page, "/w/acme/docker");
  await page.getByRole("main").getByRole("combobox").first().selectOption({ label: "web-1 · docker" });
  await expect(page.getByText("shop-api-1").first()).toBeVisible();
  await expect(page.getByText("18.72%").first()).toBeVisible();
  await shot(page, "docker");
});

test("logs", async ({ page }) => {
  await open(page, "/w/acme/logs");
  await page.getByRole("combobox", { name: "Host" }).selectOption("web-1");
  await page.getByRole("button", { name: "Container" }).click();
  await page.getByRole("combobox", { name: "Container" }).selectOption("shop-api-1");
  await page.getByRole("button", { name: "Stream" }).click();
  await expect(page.getByText("giving up after 5 retries").first()).toBeVisible();
  await shot(page, "logs");
});

test("terminal", async ({ page }) => {
  await open(page, "/w/acme/terminal");
  await page.getByRole("main").getByText("web-1", { exact: true }).first().click();
  const term = page.locator(".xterm").first();
  await expect(term).toBeVisible();
  await expect(term).toContainText("deploy@web-1", { timeout: 20_000 });
  await term.click();
  await page.keyboard.type("docker compose -p shop ps --format table\n");
  await page.waitForTimeout(800);
  await page.keyboard.type("uptime\n");
  await shot(page, "terminal");
});

test("vault", async ({ page }) => {
  await open(page, "/w/acme/vault");
  await page.getByText("grafana-admin").click();
  await expect(page.getByText("otpauth", { exact: false }).or(page.getByText(/\d{3} ?\d{3}/)).first()).toBeVisible();
  await shot(page, "vault");
});

test("ansible", async ({ page }) => {
  await open(page, "/w/acme/ansible");
  await page.getByText("site.yml").click();
  await expect(page.getByText("Install Docker").first()).toBeVisible();
  await shot(page, "ansible");
});

test("settings: local agents", async ({ page }) => {
  await open(page, "/settings");
  await page.getByRole("button", { name: /Claude Code/ }).first().click();
  const models = page.getByText(/reported by the CLI/);
  await expect(models).toBeVisible({ timeout: 30_000 });
  await models.scrollIntoViewIfNeeded();
  await shot(page, "settings-agents");
});

test("agent access", async ({ page }) => {
  await open(page, "/settings");
  await page.getByRole("button", { name: "Connect an agent" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("textbox", { name: /^Label/ }).fill("Claude Code on my laptop");
  await dialog.getByRole("button", { name: "Make the key" }).click();
  await expect(page.getByText("This is the only time the key is shown")).toBeVisible();
  await shot(page, "agent-access");
});

test("databases", async ({ page }) => {
  test.skip(!db, "no -postgres");
  await open(page, "/w/acme/databases");
  const editor = page.locator(".cm-content").first();
  await editor.click();
  await page.keyboard.press("Control+a");
  await page.keyboard.type("select id, status, total, shipping, created_at\nfrom orders\nwhere status <> 'refunded'\norder by created_at desc;");
  await page.getByRole("button", { name: "Run", exact: true }).click();
  const cell = page.getByRole("main").getByText(/"carrier"/).nth(1);
  await expect(cell).toBeVisible();
  await cell.dblclick();
  await expect(page.getByText('"tracking"').first()).toBeVisible();
  await shot(page, "databases");
});

test("databases: a change asks first", async ({ page }) => {
  test.skip(!db, "no -postgres");
  await open(page, "/w/acme/databases");
  const editor = page.locator(".cm-content").first();
  await editor.click();
  await page.keyboard.press("Control+a");
  await page.keyboard.type("update orders set status = 'refunded'\nwhere id = 42 and status = 'paid';");
  await page.getByRole("button", { name: "Run", exact: true }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await shot(page, "databases-confirm");
});

test("activity", async ({ page }) => {
  await open(page, "/w/acme/activity");
  await expect(page.getByText("resource.create").first()).toBeVisible();
  await shot(page, "activity");
});

test("topology", async ({ page }) => {
  await open(page, "/w/acme/topology");
  await expect(page.locator(".react-flow__node").first()).toBeVisible();
  await page.getByRole("button", { name: "Auto-arrange" }).click();
  await page.waitForTimeout(1500);
  await page.locator(".react-flow__controls-fitview").click();
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("unsaved")).toBeHidden();
  for (const b of await page.getByRole("button", { name: "Close" }).all()) await b.click().catch(() => {});
  await page.waitForTimeout(800);
  await shot(page, "topology");
});
