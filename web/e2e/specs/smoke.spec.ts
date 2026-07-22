import { test, expect, registerPlayer } from "../fixtures";

test.describe("smoke", () => {
  test("admin can reach the challenges console", async ({ adminPage }) => {
    await adminPage.goto("/admin/challenges");
    await expect(adminPage.getByRole("heading", { name: "Challenges" })).toBeVisible();
    await expect(adminPage.getByRole("button", { name: "New challenge" }).first()).toBeVisible();
  });

  test("a player can register and land on a team", async ({ browser }) => {
    const suffix = Date.now();
    const player = await registerPlayer(browser, {
      name: `Smoke ${suffix}`,
      email: `smoke-${suffix}@example.com`,
      password: "player-secret-123",
      team: `Smoke Team ${suffix}`,
    });
    await expect(player.page.getByRole("heading", { name: `Smoke Team ${suffix}` })).toBeVisible();
    await player.context.close();
  });
});
