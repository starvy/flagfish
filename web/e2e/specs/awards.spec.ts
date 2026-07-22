import { test, expect, registerPlayer } from "../fixtures";

async function scoreOf(page: import("@playwright/test").Page, name: string): Promise<number> {
  const sb = (await (await page.request.get("/api/v1/scoreboard")).json()) as {
    standings: Array<{ name: string; score: number }>;
  };
  return sb.standings.find((s) => s.name === name)?.score ?? 0;
}

// Manual awards/penalties: grant a positive award and a negative penalty to a team, confirm the
// scoreboard total moves and the audit trail records it, then revoke one.
test.describe("manual awards and penalties", () => {
  test("grant, penalise, audit, and revoke move the scoreboard", async ({ adminPage, browser }) => {
    const s = Date.now();
    const teamName = `Award Team ${s}`;
    const player = await registerPlayer(browser, {
      name: `Award P ${s}`,
      email: `award-${s}@example.com`,
      password: "player-secret-123",
      team: teamName,
    });
    const team = (await (await player.page.request.get("/api/v1/me/team")).json()) as { id: number };

    await adminPage.goto(`/admin/teams/${team.id}`);
    const grant = adminPage.locator(".ff-card", { hasText: "adjustment" }).first();

    // Positive award.
    await adminPage.getByLabel("Points").fill("500");
    await adminPage.getByLabel("Reason").fill(`e2e bonus ${s}`);
    await adminPage.getByRole("button", { name: "Apply adjustment" }).click();
    await expect(adminPage.locator(".ff-toast__title", { hasText: /Granted 500 points/ })).toBeVisible();
    await expect.poll(() => scoreOf(adminPage, teamName)).toBe(500);

    // Negative penalty.
    await adminPage.getByLabel("Points").fill("-200");
    await adminPage.getByLabel("Reason").fill(`e2e penalty ${s}`);
    await adminPage.getByRole("button", { name: "Apply adjustment" }).click();
    await expect(adminPage.locator(".ff-toast__title", { hasText: /Penalised 200 points/ })).toBeVisible();
    await expect.poll(() => scoreOf(adminPage, teamName)).toBe(300);
    void grant;

    // Audit records the adjustments.
    await adminPage.goto("/admin/audit");
    const auditRows = adminPage.getByRole("row").filter({ hasText: /award|adjust/i });
    await expect(auditRows.first()).toBeVisible();

    // Revoke the penalty specifically; the scoreboard moves back to +500.
    await adminPage.goto(`/admin/teams/${team.id}`);
    const penaltyRow = adminPage.locator(".ff-award-row").filter({ hasText: `e2e penalty ${s}` });
    await penaltyRow.getByRole("button", { name: "Revoke" }).click();
    await adminPage.getByRole("dialog").getByRole("button", { name: "Revoke" }).click();
    await expect(adminPage.locator(".ff-toast__title", { hasText: "Adjustment revoked" })).toBeVisible();
    await expect.poll(() => scoreOf(adminPage, teamName)).toBe(500);

    await player.context.close();
  });
});
