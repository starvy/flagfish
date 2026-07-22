import { readFileSync } from "node:fs";
import { test, expect, registerFreshPlayer, uniq } from "../fixtures";
import { seedChallenge, seedFlag } from "../api";
import { submitFlag } from "../helpers";
import type { Page } from "@playwright/test";

async function downloadText(page: Page, linkName: string): Promise<{ name: string; text: string }> {
  const exports = page.getByRole("navigation", { name: "CSV exports" });
  const [download] = await Promise.all([
    page.waitForEvent("download"),
    exports.getByRole("link", { name: linkName }).click(),
  ]);
  const path = await download.path();
  return { name: download.suggestedFilename(), text: readFileSync(path, "utf8") };
}

// The organiser's export: a real file download of the final standings and the user roster, each a
// CSV with a header and at least the row we just created.
test.describe("ops — CSV export", () => {
  test("standings and users export as downloadable CSV files with rows", async ({
    adminPage,
    browser,
  }) => {
    // Guarantee at least one scored account so both exports have a data row.
    const flag = `flag{${uniq("csv")}}`;
    const id = await seedChallenge(adminPage, { name: uniq("Scored"), category: "misc", value: 100 });
    await seedFlag(adminPage, id, { content: flag });

    const { page, player } = await registerFreshPlayer(browser, "exportee");
    try {
      await page.goto(`/challenges/${id}`);
      await submitFlag(page, flag);
      await expect(page.getByText("+100 points.")).toBeVisible();

      await adminPage.goto("/admin");

      const standings = await downloadText(adminPage, "Final standings");
      expect(standings.name).toMatch(/\.csv$/);
      const standingRows = standings.text.trim().split(/\r?\n/);
      expect(standingRows.length).toBeGreaterThanOrEqual(2); // header + at least one entrant
      expect(standingRows[0]).toContain(",");
      expect(standings.text).toContain(player.name);

      const users = await downloadText(adminPage, "Users");
      expect(users.name).toMatch(/\.csv$/);
      const userRows = users.text.trim().split(/\r?\n/);
      expect(userRows.length).toBeGreaterThanOrEqual(2);
      expect(users.text).toContain(player.email);
    } finally {
      await page.context().close();
    }
  });
});
