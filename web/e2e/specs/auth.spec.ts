import { test, expect, registerPlayer } from "../fixtures";

// Register / login / logout — the happy path and the three rejections a real event hits daily:
// a second person on the same address, a too-short password, and a fat-fingered login.

test("register creates an authed session", async ({ browser }) => {
  const s = Date.now();
  const name = `Happy ${s}`;
  const { context, page } = await registerPlayer(browser, {
    name,
    email: `happy-${s}@example.com`,
    password: "HappyPass123",
  });

  expect(new URL(page.url()).pathname).not.toBe("/register");
  // The shell shows the account name once authed, whatever the team state.
  await expect(page.getByRole("button", { name })).toBeVisible();
  await context.close();
});

test("registration refuses a duplicate email", async ({ browser }) => {
  const s = Date.now();
  const email = `dup-${s}@example.com`;
  const first = await registerPlayer(browser, { name: `Dup One ${s}`, email, password: "DupPass1234" });

  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto("/register");
  await page.getByLabel("Name").fill(`Dup Two ${s}`);
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill("DupPass1234");
  await page.getByRole("button", { name: "Create account" }).click();

  // A taken address keeps the form on screen with a loud error; it must never mint a second account.
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Create an account" })).toBeVisible();
  expect(new URL(page.url()).pathname).toBe("/register");

  await first.context.close();
  await context.close();
});

test("registration refuses a too-short password", async ({ browser }) => {
  const s = Date.now();
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto("/register");
  await page.getByLabel("Name").fill(`Weak ${s}`);
  await page.getByLabel("Email").fill(`weak-${s}@example.com`);
  await page.getByLabel("Password").fill("short"); // under the 8-char minimum
  await page.getByRole("button", { name: "Create account" }).click();

  // The form validates nothing itself (noValidate) — the server is the authority, and it refuses.
  await expect(page.getByRole("alert")).toBeVisible();
  expect(new URL(page.url()).pathname).toBe("/register");
  await context.close();
});

test("login rejects a wrong password", async ({ browser }) => {
  const s = Date.now();
  const email = `login-${s}@example.com`;
  const player = await registerPlayer(browser, { name: `Login ${s}`, email, password: "RightPass123" });

  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto("/login");
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill("WrongPass123");
  await page.getByRole("button", { name: "Sign in" }).click();

  await expect(page.getByRole("alert")).toBeVisible();
  expect(new URL(page.url()).pathname).toBe("/login");

  await player.context.close();
  await context.close();
});

test("logout ends the session and a protected page redirects to login", async ({ browser }) => {
  const s = Date.now();
  const email = `logout-${s}@example.com`;
  const { context, page } = await registerPlayer(browser, {
    name: `Logout ${s}`,
    email,
    password: "LogoutPass123",
  });

  // Open the user menu and sign out.
  await page.getByRole("button", { name: `Logout ${s}` }).click();
  await page.getByRole("menuitem", { name: "log out" }).click();
  await page.waitForURL((url) => url.pathname.startsWith("/login"));

  // The session is gone: a protected page bounces back to login rather than rendering.
  await page.goto("/settings");
  await page.waitForURL((url) => url.pathname.startsWith("/login"));
  expect(new URL(page.url()).pathname).toBe("/login");

  await context.close();
});
