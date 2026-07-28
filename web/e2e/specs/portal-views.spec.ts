import { test, expect, createChallengeWithFlag, registerPlayer, submitFlag, uniq } from "../fixtures";
import { patchConfig, removeAnnotation, seedAnnotation } from "../api";

// Portal views: the admin picks how the challenge board is drawn, and a player can take the plain
// board back for their own browser. The globe places a challenge in the country it is annotated
// with, and everything the map cannot place stays listed beside it.
//
// The scene is asserted through the DOM — the scene container, the pins, the drawer — never
// through pixels. A headless browser renders WebGL on a software rasteriser, and what it puts on
// screen is not what a GPU would.

// Forces the software rasteriser so a headless Chromium reports WebGL support. Has to sit at the
// top level — Playwright refuses launchOptions inside a describe group. Older Chromium builds
// without it fall back to "no WebGL", where the globe correctly refuses to draw and these specs
// would be asserting the fallback instead.
test.use({
  launchOptions: { args: ["--use-gl=angle", "--use-angle=swiftshader", "--enable-unsafe-swiftshader"] },
});

test.describe("portal views", () => {
  // The view is instance-wide, so it has to be handed back however the test ends. Everything
  // after this spec expects the standard board. The annotations go too: the one-pin fast path
  // asserts CZ holds exactly one placed challenge, and a leftover from an earlier run would
  // turn the pin into a country list. The challenges themselves stay — one that got solved
  // cannot be deleted (solves are kept), and unplaced they no longer count.
  const annotated: number[] = [];
  test.afterEach(async ({ adminPage }) => {
    await patchConfig(adminPage, { portal_view: "standard" });
    for (const id of annotated.splice(0)) {
      await removeAnnotation(adminPage, id, "country");
    }
  });

  test("an admin switches the board to the globe, and a player can switch back", async ({
    adminPage,
    browser,
  }) => {
    const name = uniq("Globe");
    const id = await createChallengeWithFlag(adminPage, { name, flag: `flag{${uniq("g")}}` });
    await seedAnnotation(adminPage, id, "country", "CZ");
    annotated.push(id);

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
    annotated.push(placedId);

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
      // The globe honours prefers-reduced-motion by not auto-rotating. Without this the pin is
      // a target in constant motion, and a click waits forever for its box to hold still.
      await page.emulateMedia({ reducedMotion: "reduce" });
      await page.goto("/challenges");
      await expect(page.getByTestId("globe-scene")).toBeVisible();

      // The pin is an HTML element the globe positions over the canvas, so it only exists once
      // the scene has laid out.
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

      // The drawer is modal — nothing behind it is clickable until it goes. The ✕ is Dialog
      // chrome, a sibling of the drawer body, so it is found from the page.
      await page.getByRole("button", { name: "Close dialog" }).click();
      await expect(drawer).toHaveCount(0);

      // A view never hides what the API returned.
      await page.getByRole("button", { name: /not on the map/i }).click();
      await expect(page.getByRole("link", { name: drifting })).toBeVisible();
    } finally {
      await context.close();
    }
  });
});
