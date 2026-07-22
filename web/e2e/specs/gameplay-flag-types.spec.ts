import { test, expect, registerFreshPlayer, uniq } from "../fixtures";
import { seedChallenge, seedFlag } from "../api";
import { submitFlag } from "../helpers";

const INCORRECT = "not the flag. the attempt was recorded.";

// Flag judging is the product. These prove the two non-trivial flag kinds decide correctly, and
// that a flag the server cannot compile is refused loudly at authoring time — never stored to
// silently mark a correct answer wrong for the whole event.
test.describe("gameplay — flag types", () => {
  test("a regex flag accepts a match and rejects a miss", async ({ adminPage, browser }) => {
    const value = 200;
    const id = await seedChallenge(adminPage, { name: uniq("Regex Chal"), category: "web", value });
    // Any five-or-more-digit token inside flag{…}.
    await seedFlag(adminPage, id, { type: "regex", content: "^flag\\{[0-9]{5,}\\}$" });

    const { page } = await registerFreshPlayer(browser, "regex");
    try {
      await page.goto(`/challenges/${id}`);

      await submitFlag(page, "flag{abcde}"); // shape matches, but not digits
      await expect(page.getByText(INCORRECT)).toBeVisible();

      await submitFlag(page, "flag{01234}"); // matches the pattern
      await expect(page.getByText(`+${value} points.`)).toBeVisible();
    } finally {
      await page.context().close();
    }
  });

  test("a case-insensitive static flag accepts different casing", async ({ adminPage, browser }) => {
    const value = 150;
    const id = await seedChallenge(adminPage, { name: uniq("CI Chal"), category: "misc", value });
    await seedFlag(adminPage, id, { content: "flag{CaseMatters}", caseInsensitive: true });

    const { page } = await registerFreshPlayer(browser, "ci");
    try {
      await page.goto(`/challenges/${id}`);
      await submitFlag(page, "FLAG{casematters}"); // different case, still correct
      await expect(page.getByText(`+${value} points.`)).toBeVisible();
    } finally {
      await page.context().close();
    }
  });

  test("a case-sensitive static flag rejects different casing", async ({ adminPage, browser }) => {
    const value = 150;
    const id = await seedChallenge(adminPage, { name: uniq("CS Chal"), category: "misc", value });
    await seedFlag(adminPage, id, { content: "flag{ExactOnly}", caseInsensitive: false });

    const { page } = await registerFreshPlayer(browser, "cs");
    try {
      await page.goto(`/challenges/${id}`);
      await submitFlag(page, "flag{exactonly}");
      await expect(page.getByText(INCORRECT)).toBeVisible();
      await submitFlag(page, "flag{ExactOnly}");
      await expect(page.getByText(`+${value} points.`)).toBeVisible();
    } finally {
      await page.context().close();
    }
  });

  test("a non-compiling regex is refused loudly, not stored", async ({ adminPage }) => {
    const id = await seedChallenge(adminPage, { name: uniq("Bad Regex"), category: "misc", value: 100 });
    // Unbalanced group: invalid under the server's RE2 engine. The write must fail — a stored
    // broken pattern would judge every correct flag "incorrect" in silence.
    await expect(
      seedFlag(adminPage, id, { type: "regex", content: "flag\\{(unterminated" }),
    ).rejects.toThrow(/4\d\d/);
  });
});
