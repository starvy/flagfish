import { test, expect, registerFreshPlayer, uniq } from "../fixtures";
import { seedChallenge, seedFlag, seedRequirements } from "../api";
import { submitFlag } from "../helpers";

// Prerequisites gate a challenge until its unlock is earned. This proves the gate holds on the
// board and on the detail page, that the flag cannot be judged while locked, and that solving the
// prerequisite lifts it — plus the visible difference between a previewed lock (real name shown)
// and a masked one (name replaced with ???).
test.describe("gameplay — prerequisites", () => {
  test("a locked challenge unlocks only after its prerequisite is solved", async ({
    adminPage,
    browser,
  }) => {
    const gateFlag = `flag{${uniq("gate")}}`;
    const targetFlag = `flag{${uniq("target")}}`;

    const gateName = uniq("Gate");
    const gateId = await seedChallenge(adminPage, { name: gateName, category: "intro", value: 100 });
    await seedFlag(adminPage, gateId, { content: gateFlag });

    const previewName = uniq("Preview Target");
    const previewId = await seedChallenge(adminPage, {
      name: previewName,
      category: "intro",
      value: 200,
    });
    await seedFlag(adminPage, previewId, { content: targetFlag });
    await seedRequirements(adminPage, previewId, [gateId], "preview");

    const maskedName = uniq("Masked Target");
    const maskedId = await seedChallenge(adminPage, {
      name: maskedName,
      category: "intro",
      value: 50,
    });
    await seedFlag(adminPage, maskedId, { content: `flag{${uniq("masked")}}` });
    await seedRequirements(adminPage, maskedId, [gateId], "masked");

    const { page } = await registerFreshPlayer(browser, "prereq");
    try {
      // On the board, preview shows its real name; masked hides behind ??? — and the masked name
      // never appears.
      await page.goto("/challenges");
      await expect(page.getByText(previewName)).toBeVisible();
      await expect(page.getByText("???").first()).toBeVisible();
      await expect(page.getByText(maskedName)).toHaveCount(0);

      // The previewed target is readable but locked: the flag form is shut and the lock is stated.
      await page.goto(`/challenges/${previewId}`);
      await expect(page.getByText("locked").first()).toBeVisible();
      await expect(page.getByLabel("flag", { exact: true })).toBeDisabled();

      // The server refuses a flag on a locked challenge even if the form is bypassed.
      const status = await page.evaluate(async (id) => {
        const me = (await (await fetch("/api/v1/me", { credentials: "include" })).json()) as {
          csrf_token: string;
        };
        const res = await fetch(`/api/v1/challenges/${id}/attempt`, {
          method: "POST",
          credentials: "include",
          headers: { "Content-Type": "application/json", "CSRF-Token": me.csrf_token },
          body: JSON.stringify({ flag: "flag{anything}" }),
        });
        return res.status;
      }, previewId);
      expect(status).toBe(403);

      // Solve the prerequisite.
      await page.goto(`/challenges/${gateId}`);
      await submitFlag(page, gateFlag);
      await expect(page.getByText("+100 points.")).toBeVisible();

      // The target is now open: no lock, form enabled, flag judged.
      await page.goto(`/challenges/${previewId}`);
      await expect(page.getByText("locked")).toHaveCount(0);
      await expect(page.getByLabel("flag", { exact: true })).toBeEnabled();
      await submitFlag(page, targetFlag);
      await expect(page.getByText("+200 points.")).toBeVisible();

      // And the masked one has come out of hiding under its real name.
      await page.goto("/challenges");
      await expect(page.getByText(maskedName)).toBeVisible();
    } finally {
      await page.context().close();
    }
  });
});
