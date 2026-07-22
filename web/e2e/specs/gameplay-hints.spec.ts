import { test, expect, registerFreshPlayer, uniq } from "../fixtures";
import { seedChallenge, seedFlag, seedHint } from "../api";
import { submitFlag } from "../helpers";
import type { Page } from "@playwright/test";

async function selfScore(page: Page, userId: number): Promise<string> {
  await page.goto(`/users/${userId}`);
  return (await page.locator(".ff-team-score").innerText()).trim();
}

// Hints cost points, unlock in a prerequisite chain, and a free one costs nothing. This drives all
// three through the real confirm-and-charge dialog and proves the score moves exactly as promised.
test.describe("gameplay — hints", () => {
  test("free, paid and prerequisite-gated hints behave as billed", async ({
    adminPage,
    browser,
  }) => {
    // A bank of points to spend.
    const bankFlag = `flag{${uniq("bank")}}`;
    const bankId = await seedChallenge(adminPage, { name: uniq("Bank"), category: "misc", value: 100 });
    await seedFlag(adminPage, bankId, { content: bankFlag });

    const freeBody = `free-body-${uniq("f")}`;
    const paidBody = `paid-body-${uniq("p")}`;
    const gatedBody = `gated-body-${uniq("g")}`;

    const hintChalId = await seedChallenge(adminPage, {
      name: uniq("Hinted"),
      category: "misc",
      value: 500,
    });
    await seedFlag(adminPage, hintChalId, { content: `flag{${uniq("h")}}` });
    await seedHint(adminPage, hintChalId, { title: "Free Hint", content: freeBody, cost: 0, position: 0 });
    const paidHintId = await seedHint(adminPage, hintChalId, {
      title: "Paid Hint",
      content: paidBody,
      cost: 30,
      position: 1,
    });
    await seedHint(adminPage, hintChalId, {
      title: "Gated Hint",
      content: gatedBody,
      cost: 10,
      position: 2,
      prerequisites: [paidHintId],
    });

    const { page } = await registerFreshPlayer(browser, "hints");
    try {
      const userId = await page.evaluate(
        async () =>
          (
            (await (await fetch("/api/v1/me", { credentials: "include" })).json()) as {
              user_id: number;
            }
          ).user_id,
      );

      // Earn the bank.
      await page.goto(`/challenges/${bankId}`);
      await submitFlag(page, bankFlag);
      await expect(page.getByText("+100 points.")).toBeVisible();
      expect(await selfScore(page, userId)).toBe("100 pts");

      await page.goto(`/challenges/${hintChalId}`);
      const rowOf = (title: string) => page.locator(".hint-row", { hasText: title });

      // The gated hint is locked until its prerequisite is bought.
      await expect(rowOf("Gated Hint")).toHaveClass(/hint-row--locked/);
      await expect(rowOf("Gated Hint").getByRole("button", { name: "unlock" })).toBeDisabled();

      // Free hint: confirm at zero cost, body revealed, score untouched.
      await rowOf("Free Hint").getByRole("button", { name: "unlock" }).click();
      await page.getByRole("button", { name: "unlock for 0 pts" }).click();
      await expect(page.getByText(freeBody)).toBeVisible();
      expect(await selfScore(page, userId)).toBe("100 pts");

      // Paid hint: confirm at 30, body revealed, score drops.
      await page.goto(`/challenges/${hintChalId}`);
      await rowOf("Paid Hint").getByRole("button", { name: "unlock" }).click();
      await page.getByRole("button", { name: "unlock for 30 pts" }).click();
      await expect(page.getByText(paidBody)).toBeVisible();
      expect(await selfScore(page, userId)).toBe("70 pts");

      // With the prerequisite bought, the gated hint is now buyable.
      await page.goto(`/challenges/${hintChalId}`);
      await expect(rowOf("Gated Hint")).not.toHaveClass(/hint-row--locked/);
      await expect(rowOf("Gated Hint").getByRole("button", { name: "unlock" })).toBeEnabled();
    } finally {
      await page.context().close();
    }
  });
});
