import { expect, test } from "@playwright/test";
import { loginThroughPds } from "./login";

// About is available through Settings with or without an authenticated session.
const HTTPS_URL = process.env.SERVER_HTTPS_URL;

test("about-link: signed out, settings opens About and survives a reload", async ({
  page,
}) => {
  await page.goto("/");
  await page
    .getByRole("link", { name: "Settings", exact: true })
    .first()
    .click();
  await page.getByRole("link", { name: "About", exact: true }).click();
  await expect(page).toHaveURL(/\/settings\/about$/);
  await expect(
    page.getByRole("heading", { name: "About", exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "About", exact: true }),
  ).toBeVisible();
});

test("about-link: signed in, About stays in the settings menu", async ({
  page,
}) => {
  test.skip(!HTTPS_URL, "harness started without its HTTPS hostnames");
  await loginThroughPds(page);

  await page.goto(`${HTTPS_URL}/settings`);
  await page.getByRole("link", { name: "About", exact: true }).click();
  await expect(page).toHaveURL(/\/settings\/about$/);
  await expect(
    page.getByRole("heading", { name: "About", exact: true }),
  ).toBeVisible();
});
