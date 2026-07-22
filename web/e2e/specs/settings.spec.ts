import { test, expect, registerPlayer } from "../fixtures";

// Settings: the everyday profile edits — display name, affiliation, language — and the one thing
// that actually matters about them, that they survive a reload.

test("profile edits persist across a reload", async ({ browser }) => {
  const s = Date.now();
  const newName = `Renamed ${s}`;
  const affiliation = `Crew ${s}`;

  const { context, page } = await registerPlayer(browser, {
    name: `Settings ${s}`,
    email: `settings-${s}@example.com`,
    password: "SettingsPw123",
  });

  await page.goto("/settings?tab=profile");

  // Display name lives on its own form and only enables once it differs.
  await page.getByLabel("Display name").fill(newName);
  await page.getByRole("button", { name: "Change name" }).click();
  await expect(page.locator(".ff-toast__title", { hasText: "Name changed" })).toBeVisible();

  // Affiliation and language share the profile form.
  await page.getByLabel("Affiliation").fill(affiliation);
  await page.getByLabel("Language").selectOption("de");
  await page.getByRole("button", { name: "Save profile" }).click();
  await expect(page.locator(".ff-toast__title", { hasText: "Profile saved" })).toBeVisible();

  // Reload and read it all back from the server.
  await page.goto("/settings?tab=profile");
  await expect(page.getByLabel("Display name")).toHaveValue(newName);
  await expect(page.getByLabel("Affiliation")).toHaveValue(affiliation);
  await expect(page.getByLabel("Language")).toHaveValue("de");
  // The renamed identity also reaches the shell.
  await expect(page.getByRole("button", { name: newName })).toBeVisible();

  await context.close();
});
