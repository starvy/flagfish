import { test, expect, createChallengeWithFlag, registerPlayer, submitFlag, uniq } from "../fixtures";
import { patchConfig, seedAnnotation } from "../api";

// Portal views: the admin picks how the challenge board is drawn, and a player can take the plain
// board back for their own browser. The globe places a challenge in the country it is annotated
// with, and everything the map cannot place stays listed beside it.
//
// The scene is asserted through the DOM — the scene container, the pins, the drawer — never
// through pixels. A headless browser renders WebGL on a software rasteriser, and what it puts on
// screen is not what a GPU would.

test.describe("portal views", () => {
  // SPECULATIVE: forces the software rasteriser so a headless Chromium reports WebGL support.
  // Recent Chromium builds enable SwiftShader by default and this is redundant; older ones fall
  // back to "no WebGL", where the globe correctly refuses to draw and these specs would be
  // asserting the fallback instead. Drop this line if the suite's Chromium does not need it.
  test.use({
    launchOptions: { args: ["--use-gl=angle", "--use-angle=swiftshader", "--enable-unsafe-swiftshader"] },
  });

  // The view is instance-wide, so it has to be handed back however the test ends. Everything
  // after this spec expects the standard board.
  test.afterEach(async ({ adminPage }) => {
    await patchConfig(adminPage, { portal_view: "standard" });
  });

  test("an admin switches the board to the globe, and a player can switch back", async ({
    adminPage,
    browser,
  }) => {
    const name = uniq("Globe");
    const id = await createChallengeWithFlag(adminPage, { name, flag: `flag{${uniq("g")}}` });
    await seedAnnotation(adminPage, id, "country", "CZ");

    await patchConfig(adminPage, { portal_view: "globe" });

    const { context, page } = await registerPlayer(browser, {
      name: uniq("Globetrotter"),
      email: `${uniq("globe")}@example.com`,
      password: "GlobePass123!",
      team: uniq("Globe Crew"),
    });

    try {
      await page.goto("/challenges");

      // The globe is what the instance asked for, and it is really a WebGL scene.
      await expect(page.getByTestId("globe-scene")).toBeVisible();
      await expect(page.locator('[data-testid="globe-scene"] canvas')).toBeVisible();

      // The way out is in the view itself.
      await page.getByTestId("portal-view-standard").click();
      await expect(page.getByTestId("globe-scene")).toHaveCount(0);
      await expect(page.getByRole("link", { name })).toBeVisible();

      // The opt-out is per browser and survives a reload — the instance is still on the globe.
      await page.reload();
      await expect(page.getByRole("link", { name })).toBeVisible();
      await expect(page.getByTestId("globe-scene")).toHaveCount(0);

      // The opt-out is one browser's, not the account's: a fresh context still gets the globe.
      const fresh = await browser.newContext();
      try {
        const other = await fresh.newPage();
        await other.goto("/challenges");
        // Signed out, so this is the login page — the point is only that nothing was carried over.
        await expect(other.evaluate(() => localStorage.getItem("flagfish.portalView"))).resolves.toBeNull();
      } finally {
        await fresh.close();
      }
    } finally {
      await context.close();
    }
  });

  test("a placed challenge is played from its pin, an unplaced one from the panel", async ({
    adminPage,
    browser,
  }) => {
    const placed = uniq("Prague");
    const flag = `flag{${uniq("cz")}}`;
    const value = 100;
    const placedId = await createChallengeWithFlag(adminPage, { name: placed, flag, value });
    await seedAnnotation(adminPage, placedId, "country", "CZ");

    // No country: it must still be reachable, or the view has hidden a challenge.
    const drifting = uniq("Nowhere");
    await createChallengeWithFlag(adminPage, { name: drifting, flag: `flag{${uniq("n")}}` });

    await patchConfig(adminPage, { portal_view: "globe" });

    const { context, page } = await registerPlayer(browser, {
      name: uniq("Pinner"),
      email: `${uniq("pin")}@example.com`,
      password: "PinPass123!",
      team: uniq("Pin Crew"),
    });

    try {
      await page.goto("/challenges");
      await expect(page.getByTestId("globe-scene")).toBeVisible();

      // SPECULATIVE: the pin is an HTML element the globe positions over the canvas, so it only
      // exists once the scene has laid out. If this proves flaky it is a wait, not a bug.
      const pin = page.getByTestId("globe-pin-CZ");
      await expect(pin).toBeVisible();

      // Czechia holds exactly one challenge, so its pin opens that challenge rather than a list.
      await pin.click();
      const drawer = page.getByTestId("globe-challenge-drawer");
      await expect(drawer).toBeVisible();
      await expect(drawer.getByRole("heading", { name: placed })).toBeVisible();

      // The whole point of reusing the detail body: submitting works in the drawer.
      await submitFlag(page, flag);
      await expect(page.getByText(`+${value} points.`)).toBeVisible();

      // A view never hides what the API returned.
      await page.getByRole("button", { name: /not on the map/i }).click();
      await expect(page.getByRole("link", { name: drifting })).toBeVisible();
    } finally {
      await context.close();
    }
  });
});
