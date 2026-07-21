import { test as base, expect, type Page } from "@playwright/test";

// The admin the spec authors as. Credentials come from the environment so the same spec runs against
// whatever instance the coordinator booted; the defaults match the local bring-up in the task.
const ADMIN_EMAIL = process.env.FLAGFISH_E2E_ADMIN_EMAIL ?? "admin@ctf.test";
const ADMIN_PASSWORD = process.env.FLAGFISH_E2E_ADMIN_PASSWORD ?? "hunter2hunter2";

// adminPage is a browser already signed in as an organiser. The login form is the real one — the
// session cookie a browser gets is minted by POST /login — so the spec exercises the shipped chain.
export const test = base.extend<{ adminPage: Page }>({
  adminPage: async ({ page }, use) => {
    await page.goto("/login");
    await page.getByLabel("Email").fill(ADMIN_EMAIL);
    await page.getByLabel("Password").fill(ADMIN_PASSWORD);
    await page.getByRole("button", { name: "Sign in" }).click();
    // The login lands somewhere inside the authed app; the admin console is reachable from there.
    await page.waitForURL((url) => !url.pathname.startsWith("/login"));
    await use(page);
  },
});

export { expect };
