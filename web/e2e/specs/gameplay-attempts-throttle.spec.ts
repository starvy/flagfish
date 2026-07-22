import { test, expect, registerFreshPlayer, uniq } from "../fixtures";
import { seedChallenge, seedFlag } from "../api";
import { submitFlag } from "../helpers";
import type { Page } from "@playwright/test";

const INCORRECT = "not the flag. the attempt was recorded.";

// Fires N wrong attempts straight at the API from inside the player page, as fast as the browser
// will, to exhaust the general rate-limit window. Returns how many the limiter refused (429).
async function floodAttempts(page: Page, challengeId: number, n: number): Promise<number> {
  return page.evaluate(
    async ({ challengeId, n }) => {
      const me = (await (await fetch("/api/v1/me", { credentials: "include" })).json()) as {
        csrf_token: string;
      };
      const one = () =>
        fetch(`/api/v1/challenges/${challengeId}/attempt`, {
          method: "POST",
          credentials: "include",
          headers: { "Content-Type": "application/json", "CSRF-Token": me.csrf_token },
          body: JSON.stringify({ flag: "flag{definitely-wrong}" }),
        }).then((r) => r.status);
      const statuses = await Promise.all(Array.from({ length: n }, one));
      return statuses.filter((s) => s === 429).length;
    },
    { challengeId, n },
  );
}

test.describe("gameplay — attempts and throttling", () => {
  test("a capped challenge refuses further attempts past the cap", async ({ adminPage, browser }) => {
    const id = await seedChallenge(adminPage, {
      name: uniq("Capped"),
      category: "misc",
      value: 100,
      maxAttempts: 3,
    });
    await seedFlag(adminPage, id, { content: "flag{the-right-one}" });

    const { page } = await registerFreshPlayer(browser, "capped");
    try {
      await page.goto(`/challenges/${id}`);
      // The budget shows the cap up front.
      await expect(page.getByText(/3 attempts/i)).toBeVisible();

      // Three wrong guesses are spent, and judged.
      for (let i = 0; i < 3; i++) {
        await submitFlag(page, `flag{wrong-${i}}`);
        await expect(page.getByText(INCORRECT)).toBeVisible();
      }

      // The fourth is refused outright — not judged, but blocked with the cap message.
      await submitFlag(page, "flag{wrong-final}");
      await expect(page.getByText("no attempts remaining for this challenge")).toBeVisible();
    } finally {
      await page.context().close();
    }
  });

  test("rapid-fire submits trip the rate limiter and the form shows it", async ({
    adminPage,
    browser,
  }) => {
    const id = await seedChallenge(adminPage, {
      name: uniq("Throttle"),
      category: "misc",
      value: 100,
      // Unlimited attempts, so the limiter — not the per-challenge cap — is what refuses these.
      maxAttempts: 0,
    });
    await seedFlag(adminPage, id, { content: "flag{whatever}" });

    const { page } = await registerFreshPlayer(browser, "throttle");
    try {
      await page.goto(`/challenges/${id}`);
      // Blow through the per-minute window (default 60) so the very next submit is limited.
      const refused = await floodAttempts(page, id, 75);
      expect(refused).toBeGreaterThan(0);

      // A submit through the form now comes back 429, which the form renders as a cooldown state.
      await submitFlag(page, "flag{one-more}");
      await expect(page.getByText("too many attempts")).toBeVisible();
      await expect(page.getByText(/try again in \d+s/)).toBeVisible();
    } finally {
      await page.context().close();
    }
  });
});
