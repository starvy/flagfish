import { test, expect, registerPlayer } from "../fixtures";

// Admin broadcasts a notification; a logged-in player receives it (checked via the notifications
// list, which is the durable record behind the SSE toast).
test.describe("notifications", () => {
  test("an admin broadcast reaches a player's notifications list", async ({ adminPage, browser }) => {
    const s = Date.now();
    const title = `Broadcast ${s}`;
    const body = `Please read this ${s}.`;

    const player = await registerPlayer(browser, {
      name: `Notif P ${s}`,
      email: `notif-${s}@example.com`,
      password: "player-secret-123",
      team: `Notif Team ${s}`,
    });

    await adminPage.goto("/admin/notifications");
    await adminPage.getByLabel("Title").fill(title);
    await adminPage.getByLabel("Body").fill(body);
    await adminPage.getByRole("button", { name: "Publish to everyone" }).click();
    await adminPage.getByRole("dialog").getByRole("button", { name: "Publish", exact: true }).click();
    await expect(adminPage.locator(".ff-toast__title", { hasText: "Published" })).toBeVisible();

    await player.page.goto("/notifications?page=1");
    await expect(player.page.getByText(title)).toBeVisible({ timeout: 15_000 });
    await player.context.close();
  });
});
