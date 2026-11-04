import { request as createRequest } from "@playwright/test";
import { loginThroughPds } from "../../e2e-web/flows/login";
import { expect, test } from "./fixtures";

const server = process.env.SERVER_URL;
const httpsServer = process.env.SERVER_HTTPS_URL;
const pdsServer = process.env.PDS_HTTPS_URL;

test.use({ actionTimeout: 30_000 });

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
  const configuration = page.getByRole("button", {
    name: "Configuration entry",
    exact: true,
  });
  await expect(configuration.first()).toBeDisabled();
  await page.getByLabel("Account DID", { exact: true }).fill(did!);
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
