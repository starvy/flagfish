import { test, expect, registerFreshPlayer, uniq } from "../fixtures";
import { seedChallenge, seedFlag, seedTag } from "../api";

// A tag is attached to a challenge, shows to players on that challenge, appears in the admin tag
// catalogue with a usage count, and can be renamed there — the rename following through to what the
// player sees.
test.describe("content — tags", () => {
  test("a tag shows on the challenge and can be renamed in the catalogue", async ({
    adminPage,
    browser,
  }) => {
    const tag = uniq("webexp");
    const renamed = uniq("pwn");
    const id = await seedChallenge(adminPage, { name: uniq("Tagged"), category: "web", value: 100 });
    await seedFlag(adminPage, id, { content: `flag{${uniq("t")}}` });
    await seedTag(adminPage, id, tag);

    const { page } = await registerFreshPlayer(browser, "tagviewer");
    try {
      // Players see the tag on the challenge.
      await page.goto(`/challenges/${id}`);
      await expect(page.getByText(tag)).toBeVisible();

      // The catalogue lists it, with the one challenge that carries it.
      await adminPage.goto("/admin/tags");
      const row = adminPage.getByRole("row", { name: new RegExp(tag) });
      await expect(row).toBeVisible();
      // The usage-count cell is exactly "1"; match the cell, not any text containing a 1 (the random
      // tag name often carries one too).
      await expect(row.getByRole("cell", { name: "1", exact: true })).toBeVisible();

      // Rename it, and confirm the catalogue now knows only the new name.
      await row.getByRole("button", { name: "Rename / merge" }).click();
      await adminPage.getByLabel("Into").fill(renamed);
      await adminPage.getByRole("button", { name: "Apply" }).click();
      await expect(adminPage.getByRole("row", { name: new RegExp(renamed) })).toBeVisible();
      await expect(adminPage.getByRole("row", { name: new RegExp(`^.*\\b${tag}\\b`) })).toHaveCount(0);

      // The rename reaches the player: the challenge now carries the new tag, not the old.
      await page.goto(`/challenges/${id}`);
      await expect(page.getByText(renamed)).toBeVisible();
      await expect(page.getByText(tag, { exact: true })).toHaveCount(0);
    } finally {
      await page.context().close();
    }
  });
});
