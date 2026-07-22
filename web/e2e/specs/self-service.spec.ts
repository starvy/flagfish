import { test, expect, registerPlayer, login } from "../fixtures";

// A player changes their own password and confirms they can still sign in with the new one.
test.describe("self-service", () => {
  test("change password, then log in with the new password", async ({ browser }) => {
    const s = Date.now();
    const email = `self-${s}@example.com`;
    const oldPassword = "player-secret-123";
    const newPassword = "player-secret-456";
    const player = await registerPlayer(browser, {
      name: `Self ${s}`,
      email,
      password: oldPassword,
      team: `Self Team ${s}`,
    });

    await player.page.goto("/change-password");
    // Name-based locators: the two "new password" labels collide on substring, and an exact label
    // match trips on the required-field asterisk baked into the label text.
    await player.page.locator('input[name="current_password"]').fill(oldPassword);
    await player.page.locator('input[name="new_password"]').fill(newPassword);
    await player.page.locator('input[name="new_password_confirm"]').fill(newPassword);
    await player.page.getByRole("button", { name: "Change password" }).click();
    await player.page.waitForURL(/\/challenges/, { timeout: 15_000 });
    await player.context.close();

    // A brand-new context (no cookies) must accept the new password.
    const fresh = await browser.newContext();
    const page = await fresh.newPage();
    await login(page, email, newPassword);
    await expect(page).toHaveURL(/\/challenges/);
    await fresh.close();
  });
});
