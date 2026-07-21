import { test, expect } from "../fixtures";

// Three challenge-play UX regressions, walked in one browser session against the real server.
//
//  1. A correct submit moves the header points badge without a reload — in teams mode the header
//     reads the team, not the board, so the solve has to invalidate that.
//  2. A hint bought with points still shows its body after a reload — the detail now carries the
//     body of hints this account has unlocked, instead of holding it only in a tab's memory.
//  3. A challenge with no files keeps the submit box on screen — the "no files" empty state is a
//     compact line, not a full-height hero that shoves the flag box below the fold.
//
// The instance is booted in teams mode with one file-less challenge (value 250) that carries a
// static flag and a single 50-point hint.
const FLAG = "flag{e2e_win}";
const HINT_BODY = "The answer is hex, not decimal.";
const CHALLENGE = "/challenges/1";

test("correct submit updates the header score, a paid hint survives reload, and a file-less challenge keeps the submit box in view", async ({
  registerPlayer,
}) => {
  const { page } = await registerPlayer();

  // Teams mode: a player scores through a team, so make one first. The page carries a create form
  // and a join form, both with a "Team name" field, so scope to the create form.
  const stamp = Date.now();
  await page.goto("/team");
  const createForm = page
    .locator("form")
    .filter({ has: page.getByRole("button", { name: "Create team" }) });
  await createForm.getByLabel("Team name").fill(`Team ${stamp}`);
  await createForm.getByLabel("Join password").fill("team-secret-123");
  // Wait on the create response itself: the button spins (its accessible name changes) the instant
  // it is clicked, so watching the button vanish would let us navigate away mid-request.
  const created = page.waitForResponse(
    (r) => r.url().endsWith("/api/v1/teams") && r.request().method() === "POST" && r.ok(),
  );
  await createForm.getByRole("button", { name: "Create team" }).click();
  await created;

  // A laptop-height viewport: with the old full-height empty state the submit box lands near 960px,
  // under the fold; the compact one keeps it comfortably inside 720.
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.goto(CHALLENGE);

  const submit = page.getByRole("button", { name: "submit" });
  const score = page.locator(".sh-usermenu__score");

  // ---- bug 3: no files, submit box still on screen ----
  // The "no files" empty state is a compact line now; the full-height version pushed the submit box
  // past the 720 fold. With the fix it stays comfortably inside the viewport.
  await expect(page.getByText("no files", { exact: true })).toBeVisible();
  await expect(submit).toBeInViewport();

  // ---- bug 1: correct submit moves the header badge with no reload ----
  await expect(score).toHaveText("0");
  await page.getByLabel("flag").fill(FLAG);
  await submit.click();
  await expect(page.getByText("+250 points.")).toBeVisible();
  // No reload between the solve and this assertion — the badge has to move on its own.
  await expect(score).toHaveText("250");

  // ---- bug 2: unlock a hint, then reload, and the body is still there ----
  await page.getByRole("button", { name: "unlock" }).click();
  await page.getByRole("button", { name: /unlock for 50/ }).click();
  await expect(page.getByText(HINT_BODY)).toBeVisible();

  await page.reload();
  // The body came from the challenge detail on this fresh load, not from the unlock response.
  await expect(page.getByText(HINT_BODY)).toBeVisible();
});
