import { test, expect, registerPlayer } from "../fixtures";

// Captain self-service, all through the player UI: kick a member, hand over captaincy, and disband
// an empty team. None of this touches the admin console.

test("a captain kicks a member and transfers captaincy", async ({ browser }) => {
  const s = Date.now();
  const teamName = `Roster ${s}`;
  const teamPassword = "RosterPw12345";

  const captain = await registerPlayer(browser, {
    name: `Cap ${s}`,
    email: `cap-${s}@example.com`,
    password: "CapPass12345",
    team: { name: teamName, password: teamPassword },
  });
  const one = await registerPlayer(browser, {
    name: `Member One ${s}`,
    email: `m1-${s}@example.com`,
    password: "MemberPw1234",
    team: { name: teamName, password: teamPassword, join: true },
  });
  const two = await registerPlayer(browser, {
    name: `Member Two ${s}`,
    email: `m2-${s}@example.com`,
    password: "MemberPw1234",
    team: { name: teamName, password: teamPassword, join: true },
  });

  const page = captain.page;
  await page.goto("/team");
  const roster = page.getByRole("table", { name: new RegExp(`Members of ${teamName}`) });
  await expect(roster.getByRole("row").filter({ hasText: `Member One ${s}` })).toBeVisible();
  await expect(roster.getByRole("row").filter({ hasText: `Member Two ${s}` })).toBeVisible();

  // Kick member one.
  await roster
    .getByRole("row")
    .filter({ hasText: `Member One ${s}` })
    .getByRole("button", { name: "Kick" })
    .click();
  await page.getByRole("dialog").getByRole("button", { name: "Remove" }).click();
  await expect(roster.getByRole("row").filter({ hasText: `Member One ${s}` })).toHaveCount(0);

  // Hand captaincy to member two.
  await roster
    .getByRole("row")
    .filter({ hasText: `Member Two ${s}` })
    .getByRole("button", { name: "Make captain" })
    .click();
  await page.getByRole("dialog").getByRole("button", { name: "Make captain" }).click();

  // Member two now wears the captain badge; the old captain has lost the captain-only controls.
  await expect(
    roster.getByRole("row").filter({ hasText: `Member Two ${s}` }).getByText("captain"),
  ).toBeVisible();
  await expect(page.getByRole("heading", { name: "Disband team" })).toHaveCount(0);

  await captain.context.close();
  await one.context.close();
  await two.context.close();
});

test("a captain disbands an empty team", async ({ browser }) => {
  const s = Date.now();
  const teamName = `Solo ${s}`;

  const captain = await registerPlayer(browser, {
    name: `Solo Cap ${s}`,
    email: `solo-${s}@example.com`,
    password: "SoloPass1234",
    team: { name: teamName, password: "SoloTeamPw123" },
  });

  const page = captain.page;
  await page.goto("/team");
  await page.getByRole("button", { name: new RegExp(`Disband ${teamName}`) }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel(/Name of the team/).fill(teamName);
  await dialog.getByRole("button", { name: "Disband team" }).click();

  // The team is gone: the page falls back to enrollment.
  await expect(page.getByRole("heading", { name: "Create a team" })).toBeVisible();

  await captain.context.close();
});
