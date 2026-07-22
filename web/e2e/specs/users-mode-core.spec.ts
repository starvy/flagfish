import { test, expect, registerFreshPlayer, uniq } from "../fixtures";
import { seedChallenge, seedFlag } from "../api";
import { submitFlag } from "../helpers";

// The whole users-mode loop: a challenge exists, a player registers with no team in sight, solves
// it, and the scoreboard ranks that individual account — with the player's own profile showing the
// score it earned.
test.describe("users mode — the core loop", () => {
  test("register, solve, and rank as an individual account", async ({ adminPage, browser }) => {
    const flag = `flag{${uniq("core")}}`;
    const value = 300;
    const name = uniq("Core Challenge");
    const id = await seedChallenge(adminPage, {
      name,
      category: "misc",
      value,
      description: "Solve me to score.",
    });
    await seedFlag(adminPage, id, { content: flag });

    const { page, player } = await registerFreshPlayer(browser, "solver");
    try {
      // No team anywhere in users mode: not in the primary nav.
      const nav = page.getByRole("navigation", { name: "Primary" });
      await expect(nav.getByRole("link", { name: "team", exact: true })).toHaveCount(0);

      await page.goto(`/challenges/${id}`);
      await submitFlag(page, flag);
      await expect(page.getByText(`+${value} points.`)).toBeVisible();

      // The scoreboard is a board of players, not teams, and the solver is on it.
      await page.goto("/scoreboard");
      await expect(page.getByRole("columnheader", { name: "player" })).toBeVisible();
      await expect(page.getByRole("columnheader", { name: "team" })).toHaveCount(0);
      const row = page.getByRole("link", { name: player.name });
      await expect(row).toBeVisible();

      // The profile behind that row shows the score the account earned and the solve that earned it.
      await row.click();
      await page.waitForURL(/\/users\/\d+$/);
      await expect(page.locator(".ff-team-score")).toHaveText(`${value} pts`);
      await expect(page.getByRole("link", { name })).toBeVisible();
    } finally {
      await page.context().close();
    }
  });

  test("the registration form asks for no team", async ({ browser }) => {
    const context = await browser.newContext();
    const page = await context.newPage();
    try {
      await page.goto("/register");
      await expect(page.getByRole("heading", { name: "Create an account" })).toBeVisible();
      await expect(page.getByLabel("Name")).toBeVisible();
      await expect(page.getByLabel("Email")).toBeVisible();
      await expect(page.getByLabel("Password")).toBeVisible();
      // None of the team affordances a teams-mode instance would show.
      await expect(page.getByLabel("Team")).toHaveCount(0);
      await expect(page.getByText(/create a team/i)).toHaveCount(0);
      await expect(page.getByText(/join a team/i)).toHaveCount(0);
    } finally {
      await context.close();
    }
  });
});
