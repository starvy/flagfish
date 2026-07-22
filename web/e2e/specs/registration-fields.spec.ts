import { test, expect, registerPlayer, ADMIN_EMAIL, ADMIN_PASSWORD } from "../fixtures";
import { request as pwRequest } from "@playwright/test";

// Custom registration fields: an organiser adds a required field and a public one; a new player must
// answer the required one to register, and the public answer sticks to their profile.

const BASE_URL = process.env.FLAGFISH_TEAMS_URL ?? "http://localhost:8019";

// Custom fields are global instance state, so a leftover required field would break every other
// spec's registration. Tear them all down no matter how the test ended.
test.afterAll(async () => {
  const ctx = await pwRequest.newContext({ baseURL: BASE_URL });
  await ctx.post("/api/v1/login", { data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD } });
  const me = (await (await ctx.get("/api/v1/me")).json()) as { csrf_token: string };
  const list = await ctx.get("/api/v1/admin/fields");
  const fields = (await list.json()).fields as { id: number }[];
  for (const f of fields) {
    await ctx.delete(`/api/v1/admin/fields/${f.id}`, { headers: { "CSRF-Token": me.csrf_token } });
  }
  await ctx.dispose();
});

async function createField(
  adminPage: import("@playwright/test").Page,
  name: string,
  flags: { required?: boolean; public?: boolean; editable?: boolean },
) {
  await adminPage.goto("/admin/fields");
  await adminPage.getByRole("button", { name: "New field" }).first().click();
  const dialog = adminPage.getByRole("dialog");
  await dialog.getByLabel(/^Name/).fill(name);
  if (flags.required) await dialog.getByLabel("Answer is mandatory").check();
  if (flags.public) await dialog.getByLabel("Answer is public").check();
  if (flags.editable) await dialog.getByLabel("Answer is editable").check();
  await dialog.getByRole("button", { name: "Create" }).click();
  await expect(adminPage.getByRole("dialog")).toBeHidden();
  await expect(adminPage.getByRole("cell", { name })).toBeVisible();
}

test("a required field gates registration and a public field sticks to the profile", async ({
  browser,
  adminPage,
}) => {
  const s = Date.now();
  const requiredField = `Ticket ID ${s}`;
  const publicField = `Homepage ${s}`;
  const homepage = `https://player-${s}.example`;

  await createField(adminPage, requiredField, { required: true });
  await createField(adminPage, publicField, { public: true, editable: true });

  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto("/register");

  // Both fields render on the form.
  await expect(page.getByLabel(requiredField)).toBeVisible();
  await expect(page.getByLabel(publicField)).toBeVisible();

  const email = `fields-${s}@example.com`;
  await page.getByLabel("Name").fill(`Fielded ${s}`);
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill("FieldsPass123");

  // Submitting with the required field blank is refused; the form stays put.
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  expect(new URL(page.url()).pathname).toBe("/register");

  // Answer both and it goes through.
  await page.getByLabel(requiredField).fill(`TICKET-${s}`);
  await page.getByLabel(publicField).fill(homepage);
  await page.getByRole("button", { name: "Create account" }).click();
  await page.waitForURL((url) => !url.pathname.startsWith("/register"));

  // The public answer is kept and shown back on the player's own profile.
  await page.goto("/settings?tab=profile");
  await expect(page.getByLabel(publicField)).toHaveValue(homepage);

  await context.close();
});
