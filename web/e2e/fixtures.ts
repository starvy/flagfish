import { test as base, expect, type Page } from "@playwright/test";

// The admin the spec authors as. Credentials come from the environment so the same spec runs against
// whatever instance the coordinator booted; the defaults match the local bring-up in the task.
const ADMIN_EMAIL = process.env.FLAGFISH_E2E_ADMIN_EMAIL ?? "admin@ctf.test";
const ADMIN_PASSWORD = process.env.FLAGFISH_E2E_ADMIN_PASSWORD ?? "hunter2hunter2";

// A fresh player, minted through the real registration form so the browser holds a session cookie the
// server issued. Returns the now-authed page and the account's name (the header shows it). The email
// is unique per call so specs never collide on a re-run against the same instance.
export type PlayerSession = { page: Page; name: string; email: string };

// adminPage is a browser already signed in as an organiser. The login form is the real one — the
// session cookie a browser gets is minted by POST /login — so the spec exercises the shipped chain.
// registerPlayer mints a brand-new player the same way, on demand.
export const test = base.extend<{
  adminPage: Page;
  registerPlayer: () => Promise<PlayerSession>;
}>({
  adminPage: async ({ page }, use) => {
    await page.goto("/login");
    await page.getByLabel("Email").fill(ADMIN_EMAIL);
    await page.getByLabel("Password").fill(ADMIN_PASSWORD);
    await page.getByRole("button", { name: "Sign in" }).click();
    // The login lands somewhere inside the authed app; the admin console is reachable from there.
    await page.waitForURL((url) => !url.pathname.startsWith("/login"));
    await use(page);
  },

  registerPlayer: async ({ page }, use) => {
    await use(async () => {
      const stamp = `${Date.now()}-${Math.floor(Math.random() * 1e6)}`;
      const name = `Player ${stamp}`;
      const email = `player-${stamp}@ctf.test`;
      await page.goto("/register");
      await page.getByLabel("Name").fill(name);
      await page.getByLabel("Email").fill(email);
      await page.getByLabel("Password").fill("correct horse battery");
      await page.getByRole("button", { name: "Create account" }).click();
      // Registration signs the browser in and lands on the board.
      await page.waitForURL((url) => url.pathname.startsWith("/challenges"));
      return { page, name, email };
    });
  },
});

export { expect };
