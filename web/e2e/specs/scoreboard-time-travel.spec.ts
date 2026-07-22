import { test, expect, registerFreshPlayer, uniq } from "../fixtures";
import { patchConfig, seedChallenge, seedFlag } from "../api";
import { submitFlag } from "../helpers";

// The scoreboard can be scrubbed back through the event. With a start two hours ago and a solve
// landing just now, dragging the handle to the far left ("as of" the opening) must show a board on
// which nobody had scored yet — the same board, a different instant.
test.describe("scoreboard — time travel", () => {
  test("scrubbing to the start shows the board before any solve", async ({ adminPage, browser }) => {
    // A start in the past is what makes the scrub range exist on the board.
    const start = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString();
    await patchConfig(adminPage, { start });

    const flag = `flag{${uniq("tt")}}`;
    const id = await seedChallenge(adminPage, { name: uniq("Timed"), category: "misc", value: 100 });
    await seedFlag(adminPage, id, { content: flag });

    const { page, player } = await registerFreshPlayer(browser, "traveler");
    try {
      await page.goto(`/challenges/${id}`);
      await submitFlag(page, flag);
      await expect(page.getByText("+100 points.")).toBeVisible();

      await page.goto("/scoreboard");
      const slider = page.locator("#ff-as-of");
      await expect(slider).toBeVisible(); // the time-travel control is present
      // Live: the solver is on the board.
      await expect(page.getByRole("link", { name: player.name })).toBeVisible();

      // Scrub to the far-left edge — the opening of the event, before anyone had scored.
      await slider.focus();
      await slider.press("Home");
      await expect(page.locator(".ff-board-travel__value")).not.toHaveText("live");
      await expect(page.getByText("as of", { exact: false })).toBeVisible();
      await expect(page.getByRole("link", { name: player.name })).toHaveCount(0);
      await expect(page.getByText("No one had scored by the instant you are looking at.")).toBeVisible();

      // Back to live restores the present board.
      await page.getByRole("button", { name: "back to live" }).click();
      await expect(page.getByRole("link", { name: player.name })).toBeVisible();
    } finally {
      await page.context().close();
    }
  });
});
