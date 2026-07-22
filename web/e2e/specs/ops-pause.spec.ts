import { test, expect, registerFreshPlayer, uniq } from "../fixtures";
import { seedChallenge, seedFlag, setPaused } from "../api";
import { submitFlag } from "../helpers";
import type { Page } from "@playwright/test";

async function pausedState(page: Page): Promise<boolean> {
  return page.evaluate(
    async () =>
      (
        (await (await fetch("/api/v1/instance", { credentials: "include" })).json()) as {
          paused: boolean;
        }
      ).paused,
  );
}

async function attemptStatus(page: Page, challengeId: number, flag: string): Promise<number> {
  return page.evaluate(
    async ({ challengeId, flag }) => {
      const me = (await (await fetch("/api/v1/me", { credentials: "include" })).json()) as {
        csrf_token: string;
      };
      const res = await fetch(`/api/v1/challenges/${challengeId}/attempt`, {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json", "CSRF-Token": me.csrf_token },
        body: JSON.stringify({ flag }),
      });
      return res.status;
    },
    { challengeId, flag },
  );
}

// The one-click incident switch. Pausing must refuse every flag — the correct one included, for
// everyone — and resuming must let it through again.
test.describe("ops — the pause switch", () => {
  test("pausing refuses flags fleet-wide, resuming restores them", async ({ adminPage, browser }) => {
    const flag = `flag{${uniq("pause")}}`;
    const id = await seedChallenge(adminPage, { name: uniq("Pausable"), category: "misc", value: 100 });
    await seedFlag(adminPage, id, { content: flag });

    const { page } = await registerFreshPlayer(browser, "pause");
    try {
      // The one-click switch lives in the console header.
      await adminPage.goto("/admin");
      // Flip the switch, through its confirmation.
      await adminPage.getByRole("button", { name: "Pause event" }).click();
      await adminPage.getByRole("button", { name: "Pause the event" }).click();
      await expect(adminPage.getByText("The event is paused.")).toBeVisible();
      await expect.poll(() => pausedState(page)).toBe(true);

      // The player sees the pause and the form is shut.
      await page.goto(`/challenges/${id}`);
      await expect(page.getByText("the CTF is paused")).toBeVisible();
      await expect(page.getByLabel("flag", { exact: true })).toBeDisabled();
      // Even the correct flag, pushed straight at the API, is refused while paused.
      expect(await attemptStatus(page, id, flag)).toBe(403);

      // Resume, again through the header confirmation.
      await adminPage.getByRole("button", { name: "Resume", exact: true }).click();
      await adminPage.getByRole("button", { name: "Resume the event" }).click();
      await expect.poll(() => pausedState(page)).toBe(false);

      // The same correct flag now scores.
      await page.goto(`/challenges/${id}`);
      await expect(page.getByText("the CTF is paused")).toHaveCount(0);
      await submitFlag(page, flag);
      await expect(page.getByText("+100 points.")).toBeVisible();
    } finally {
      // Never leave the fleet paused for the specs that follow.
      await setPaused(adminPage, false).catch(() => undefined);
      await page.context().close();
    }
  });
});
