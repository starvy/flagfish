import { test, expect, uniq } from "../fixtures";
import { seedChallenge, seedFlag, updateChallenge } from "../api";

// Every write the database sees lands in the trail. This makes an update, finds it in /admin/audit,
// and opens the before/after diff to confirm the exact field that moved is shown.
test.describe("ops — audit log", () => {
  test("an admin update appears in the audit trail with a before/after diff", async ({
    adminPage,
  }) => {
    const id = await seedChallenge(adminPage, { name: uniq("Audited"), category: "misc", value: 100 });
    await seedFlag(adminPage, id, { content: `flag{${uniq("a")}}` });
    // The mutation under audit: value 100 -> 250.
    await updateChallenge(adminPage, id, { value: 250 });

    await adminPage.goto("/admin/audit");

    // Narrow the trail to this exact challenge's UPDATE.
    await adminPage.getByLabel("Target table").fill("challenges");
    await adminPage.getByLabel("Action").selectOption("UPDATE");
    await adminPage.getByLabel("Target id").fill(String(id));
    await adminPage.getByRole("button", { name: "Apply" }).click();

    const row = adminPage.getByRole("row", { name: new RegExp(`challenges #${id}`) });
    await expect(row).toBeVisible();

    // Open the diff and reveal the guarded payload.
    await row.getByRole("button", { name: "diff" }).click();
    await expect(adminPage.getByRole("dialog")).toContainText(`UPDATE challenges #${id}`);
    await adminPage.getByRole("button", { name: "Reveal payload" }).click();

    // The before/after diff shows the old value removed and the new value added.
    const diff = adminPage.getByLabel("Before and after diff");
    await expect(diff).toBeVisible();
    await expect(diff.locator(".ff-diff__del", { hasText: "100" })).toBeVisible();
    await expect(diff.locator(".ff-diff__add", { hasText: "250" })).toBeVisible();
  });
});
