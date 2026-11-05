import { type Locator, request as createRequest } from "@playwright/test";
import { loginThroughPds } from "../../e2e-web/flows/login";
import { expect, test } from "./fixtures";

const server = process.env.SERVER_URL;
const httpsServer = process.env.SERVER_HTTPS_URL;
const pdsServer = process.env.PDS_HTTPS_URL;

test.use({ actionTimeout: 30_000 });

async function openRule(entry: Locator) {
  const details = entry.locator("xpath=ancestor::details[1]");
  if (
    !(await details.evaluate((element) => (element as HTMLDetailsElement).open))
  ) {
    await details.locator("summary").click();
  }
}

test("uses the available width for the permissions grid", async ({ page }) => {
  test.skip(!httpsServer, "requires the real TLS OAuth harness");
  await loginThroughPds(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(`${httpsServer}/settings/access`);
  const table = page.getByRole("table", {
    name: "Access entries",
    exact: true,
  });
  await expect(table).toBeVisible();
  expect((await table.boundingBox())!.width).toBeGreaterThan(1000);
  await expect(
    page.getByRole("combobox", { name: "Settings", exact: true }),
  ).toBeVisible();
  await page.goto(`${httpsServer}/settings/advanced`);
  await expect(page.locator("[data-settings-content]")).toBeVisible();
  expect(
    (await page.locator("[data-settings-content]").boundingBox())!.width,
  ).toBeLessThanOrEqual(576);
});

test("node administrators approve publishing and restrict chat independently of public viewing", async ({
  page,
  request,
}) => {
  test.skip(
    !server || !httpsServer || !pdsServer,
    "requires the real TLS PDS harness",
  );
  test.setTimeout(4 * 60_000);
  const anonymous = await request.get(
    `${server}/xrpc/place.stream.access.getStatus`,
  );
  expect(anonymous.ok()).toBe(true);
  const status = await anonymous.json();
  expect(
    status.capabilities.find(
      (entry: { capability: string }) => entry.capability === "viewer",
    ).allowed,
  ).toBe(true);
  const denied = await request.post(
    `${server}/xrpc/place.stream.access.createGrant`,
    {
      data: {
        subject: process.env.ACCOUNT_DID,
        capability: "streamer",
        effect: "allow",
      },
    },
  );
  expect(denied.status()).toBe(401);

  const pds = await createRequest.newContext({ ignoreHTTPSErrors: true });
  let did: string;
  try {
    const name = `access${Date.now().toString(36)}`;
    const created = await pds.post(
      `${pdsServer}/xrpc/com.atproto.server.createAccount`,
      {
        data: {
          handle: `${name}.${new URL(pdsServer!).hostname}`,
          email: `${name}@example.com`,
          password: process.env.ACCOUNT_PASSWORD,
        },
      },
    );
    expect(created.ok()).toBe(true);
    did = (await created.json()).did;
  } finally {
    await pds.dispose();
  }

  await loginThroughPds(page);
  await page.goto(`${httpsServer}/settings/access`);
  await expect(
    page.getByRole("heading", { name: "Node access", exact: true }),
  ).toBeVisible();
  const configuration = page
    .locator("[data-entry-id]")
    .filter({ hasText: "Configuration entry" });
  await openRule(configuration.first());
  await expect(configuration.first()).toContainText("Read-only");
  await expect(configuration.first().getByRole("button")).toHaveCount(0);
  await page.getByText("Add account", { exact: true }).click();
  await page.getByLabel("Find an account", { exact: true }).fill(did!);
  async function save(
    capability: "streamer" | "chat",
    effect: "allow" | "deny",
  ) {
    await page
      .getByRole("combobox", { name: "Capability", exact: true })
      .selectOption(capability);
    await page
      .getByRole("combobox", { name: "Action", exact: true })
      .selectOption(effect);
    const saved = page.waitForResponse((response) =>
      response.url().endsWith("/xrpc/place.stream.access.createGrant"),
    );
    await page
      .getByRole("button", { name: "Save access entry", exact: true })
      .click();
    const response = await saved;
    expect(response.ok()).toBe(true);
    return (await response.json()) as { id: string };
  }
  const allowance = await save("streamer", "allow");
  const allowRow = page.locator(`[data-entry-id="${allowance.id}"]`);
  await expect(allowRow).toContainText("Publishing permitted");
  const restriction = await save("streamer", "deny");
  const denyRow = page.locator(`[data-entry-id="${restriction.id}"]`);
  await expect(denyRow).toContainText("Publishing denied");
  await expect(allowRow).toContainText("Publishing denied");
  await openRule(denyRow);
  await denyRow
    .getByRole("button", { name: "Remove denial", exact: true })
    .click();
  await expect(denyRow).toHaveCount(0);
  await expect(allowRow).toContainText("Publishing permitted");
  await allowRow
    .getByRole("button", { name: "Revoke approval", exact: true })
    .click();
  await expect(allowRow).toHaveCount(0);

  const ban = await save("chat", "deny");
  const banRow = page.locator(`[data-entry-id="${ban.id}"]`);
  await expect(banRow).toContainText("Chat denied");
  await openRule(banRow);
  await banRow.getByRole("button", { name: "Unban chat", exact: true }).click();
  await expect(banRow).toHaveCount(0);
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Node access", exact: true }),
  ).toBeVisible();
  await expect(
    page.locator('header a[href="/settings/account"]'),
  ).toBeVisible();
});

test("administrator saves policy, reloads it, approves an account, and restores defaults", async ({
  page,
}) => {
  test.skip(!httpsServer, "requires the real TLS OAuth harness");
  await loginThroughPds(page);
  await page.goto(`${httpsServer}/settings/access`);
  await page.setViewportSize({ width: 1440, height: 900 });
  const policyEditor = page.locator("[data-access-policy-editor]");
  await policyEditor.locator(":scope > summary").click();
  const policy = page.getByRole("combobox", {
    name: "Who can stream",
    exact: true,
  });
  await expect(policy).toHaveValue("allowlist");
  await policy.selectOption("open");
  const saved = page.waitForResponse(
    (response) =>
      response.url().endsWith("/xrpc/place.stream.access.setMode") &&
      response.request().method() === "POST",
  );
  await page.locator('[data-policy-save="streamer"]').click();
  expect((await saved).ok()).toBe(true);
  await expect(page.locator('[data-policy-reset="streamer"]')).toBeVisible();
  await page.reload();
  await policyEditor.locator(":scope > summary").click();
  await expect(policy).toHaveValue("open");
  await policyEditor.locator(":scope > summary").click();
  await page
    .getByRole("button", {
      name: `Set rule: ${process.env.ACCOUNT_DID} · Chat`,
      exact: true,
    })
    .click();
  await expect(
    page.getByRole("textbox", { name: "Find an account", exact: true }),
  ).toHaveValue(process.env.ACCOUNT_DID!);
  const capability = page.getByRole("combobox", {
    name: "Capability",
    exact: true,
  });
  await expect(capability).toHaveValue("chat");
  await capability.selectOption("streamer");
  const created = page.waitForResponse(
    (response) =>
      response.url().endsWith("/xrpc/place.stream.access.createGrant") &&
      response.request().method() === "POST",
  );
  await page
    .getByRole("button", { name: "Save access entry", exact: true })
    .click();
  const response = await created;
  expect(response.ok()).toBe(true);
  const entry = await response.json();
  const row = page.locator(`[data-entry-id="${entry.id}"]`);
  await expect(row).toContainText("Publishing permitted");
  await page.getByText("Add account", { exact: true }).click();
  await page.screenshot({
    path: test.info().outputPath("node-access-desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await openRule(row);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
  await row
    .getByRole("button", { name: "Revoke approval", exact: true })
    .click();
  await expect(row).toHaveCount(0);
  await expect(page.getByText("Configuration entry").first()).toBeVisible();
  await page
    .locator(
      `[data-access-subject="${process.env.ACCOUNT_DID}"] [data-access-capability="streamer"] summary`,
    )
    .click();
  await page
    .getByRole("table", { name: "Access entries", exact: true })
    .evaluate((table) => {
      table.parentElement!.scrollLeft = 0;
    });
  await page.screenshot({
    path: test.info().outputPath("node-access-phone.png"),
    fullPage: true,
  });
  await policyEditor.locator(":scope > summary").click();
  await page.locator('[data-policy-reset="streamer"]').click();
  await expect(policy).toHaveValue("allowlist");
  await expect(page.locator('[data-policy-reset="streamer"]')).toHaveCount(0);
});
