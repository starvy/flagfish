import { test, expect } from "../fixtures";

// CMS: an admin creates a markdown page, publishes it, and an anonymous visitor can read it.
test.describe("pages / CMS", () => {
  test("create, publish, and view a public markdown page", async ({ adminPage, browser }) => {
    const s = Date.now();
    const route = `rules-${s}`;
    const title = `House Rules ${s}`;
    const marker = `no-sharing-${s}`;

    await adminPage.goto("/admin/pages");
    await adminPage.getByRole("button", { name: "New page" }).first().click();
    const dialog = adminPage.getByRole("dialog");
    await dialog.getByLabel("Route").fill(route);
    await dialog.getByLabel("Title").fill(title);
    await dialog.getByLabel("Content").fill(`# Rules\n\nRule one: ${marker}.`);
    await dialog.getByRole("checkbox", { name: "Published" }).check();
    await dialog.getByRole("button", { name: "Create", exact: true }).click();
    await expect(adminPage.locator(".ff-toast__title", { hasText: new RegExp(`Created ${title}`) })).toBeVisible();

    // A published, non-gated page is public: open it in a cookie-less context.
    const anon = await browser.newContext();
    const page = await anon.newPage();
    await page.goto(`/pages/${route}`);
    await expect(page.getByRole("heading", { name: title })).toBeVisible();
    await expect(page.getByText(marker)).toBeVisible();
    await anon.close();
  });
});
