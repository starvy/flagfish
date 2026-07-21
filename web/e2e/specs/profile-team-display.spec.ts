import { test, expect } from "../fixtures";

// Three profile/team display regressions, walked in a real browser against the booted instance
// (teams mode, a challenge #1 that takes the static flag below for 250 points — the same seed the
// challenge-play-ux spec relies on).
//
//  1. The public team page lists the team's solves — the team body now carries a solve history, and
//     the page renders it instead of gating a card on a field that was never sent.
//  2. A custom field an admin marks public shows on a profile's Details card.
//  3. The score-over-time chart carries a labelled y-axis, so a value can be read off it, not just
//     the shape.
const FLAG = "flag{e2e_win}";
const CHALLENGE = "/challenges/1";

test("a team's solves and a scaled score chart render on its public page", async ({ registerPlayer }) => {
  const { page } = await registerPlayer();

  // Teams mode: score through a team. Capture the new team's id off the create response so we can
  // open its public page directly.
  const stamp = Date.now();
  await page.goto("/team");
  const createForm = page
    .locator("form")
    .filter({ has: page.getByRole("button", { name: "Create team" }) });
  await createForm.getByLabel("Team name").fill(`Team ${stamp}`);
  await createForm.getByLabel("Join password").fill("team-secret-123");
  const created = page.waitForResponse(
    (r) => r.url().endsWith("/api/v1/teams") && r.request().method() === "POST" && r.ok(),
  );
  await createForm.getByRole("button", { name: "Create team" }).click();
  const teamId = (await (await created).json()).id as number;

  // Solve the seeded challenge through the real submit path, so the team earns a real solve.
  await page.goto(CHALLENGE);
  await page.getByLabel("flag").fill(FLAG);
  await page.getByRole("button", { name: "submit" }).click();
  await expect(page.getByText("+250 points.")).toBeVisible();

  // ---- bug 1: the public team page lists the solve ----
  await page.goto(`/teams/${teamId}`);
  const solvesCard = page
    .locator(".ff-card")
    .filter({ has: page.getByRole("heading", { name: "Solves" }) });
  const items = solvesCard.locator(".ff-timeline__item");
  await expect(items.first()).toBeVisible();
  await expect(items.first()).toContainText("250 pts");
  await expect(solvesCard.getByText("No solves yet")).toHaveCount(0);

  // ---- bug 3: the score chart carries a labelled y-axis ----
  const ticks = page.locator(".ff-scorechart__tick");
  await expect(ticks.first()).toBeVisible();
  await expect(ticks.filter({ hasText: /^0$/ }).first()).toBeVisible();
  // The top tick is the axis maximum — a value the shape alone never gave.
  const tickCount = await ticks.count();
  expect(tickCount).toBeGreaterThanOrEqual(2);
});

test("an admin-flagged public field shows on a profile", async ({ adminPage: page }) => {
  const stamp = Date.now();
  const fieldName = `School ${stamp}`;
  const answer = `MIT ${stamp}`;

  // ---- admin: define a public, editable custom field ----
  await page.goto("/admin/fields");
  await page.getByRole("button", { name: "New field" }).first().click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill(fieldName);
  await dialog.getByLabel("Answer is public").check();
  await dialog.getByLabel("Answer is editable").check();
  await dialog.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText(fieldName).first()).toBeVisible();

  // ---- answer it on the admin's own account (an admin is a user too) ----
  await page.goto("/settings");
  const fieldsCard = page
    .locator(".ff-card")
    .filter({ has: page.getByRole("heading", { name: "Registration fields" }) });
  await fieldsCard.getByLabel(fieldName).fill(answer);
  await fieldsCard.getByRole("button", { name: "Save answers" }).click();

  // ---- bug 2: the public answer surfaces on the public profile ----
  const meId = (await page.evaluate(async () => {
    const r = await fetch("/api/v1/me", { credentials: "same-origin" });
    return (await r.json()).user_id as number;
  })) as number;

  await page.goto(`/users/${meId}`);
  const details = page
    .locator(".ff-card")
    .filter({ has: page.getByRole("heading", { name: "Details" }) });
  await expect(details.getByText(fieldName)).toBeVisible();
  await expect(details.getByText(answer)).toBeVisible();
});
