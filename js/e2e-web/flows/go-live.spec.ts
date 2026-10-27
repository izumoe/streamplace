import { expect, test } from "@playwright/test";

// The DOM dashboard requires a session before exposing stream controls.
test("go-live: streaming requires login", async ({ page }) => {
  await page.goto("/dashboard");

  await expect(
    page.getByText("Please log in to access the dashboard."),
  ).toBeVisible({
    timeout: 30_000,
  });
  await page.getByRole("button", { name: "Log in", exact: true }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(
    page.getByRole("dialog").getByRole("textbox", { name: "Handle" }),
  ).toBeVisible();
});
