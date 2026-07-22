import { test, expect, createChallengeWithFlag } from "../fixtures";

// The admin challenge board actions that actually work in the SPA: a seeded challenge appears on
// the board, and delete removes it. (Editing name/value/pool needs the editor route, which is
// broken — see challenge-editor-route.spec.ts. Hiding is covered in admin-board-projection.spec.ts,
// where it exposes a projection bug.)
test.describe("challenge board actions", () => {
  test("a seeded challenge appears on the board and can be deleted", async ({ adminPage }) => {
    const name = `Board ${Date.now()}`;
    await createChallengeWithFlag(adminPage, { name, category: "web", value: 175, flag: "flag{board}" });

    await adminPage.goto("/admin/challenges");
    const row = adminPage.getByRole("row").filter({ has: adminPage.getByRole("link", { name }) });
    await expect(row).toBeVisible();
    await expect(row).toContainText("175");

    await row.getByRole("button", { name: "Delete" }).click();
    const dialog = adminPage.getByRole("dialog");
    // The destructive confirm arms only once the exact challenge name is typed.
    await dialog.getByLabel("Name of the challenge").fill(name);
    await dialog.getByRole("button", { name: "Delete", exact: true }).click();
    await expect(adminPage.locator(".ff-toast__title", { hasText: "Challenge deleted" })).toBeVisible();
    await expect(adminPage.getByRole("link", { name })).toHaveCount(0);
  });
});
