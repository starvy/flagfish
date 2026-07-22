import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { request as pwRequest, type APIRequestContext, type Page } from "@playwright/test";
import { test, expect, uniq, ADMIN_EMAIL, ADMIN_PASSWORD } from "../fixtures";

const USERS_URL = process.env.FLAGFISH_USERS_URL ?? "http://localhost:8020";

// A freshly signed-in admin API context, with no prior cookie. Anonymous POST /login is CSRF-exempt,
// so this always authenticates cleanly — unlike re-logging in on a context that already holds a
// session cookie, where /login becomes a cookie-authed write and trips the CSRF check. The caller
// disposes it.
async function freshAdmin(): Promise<APIRequestContext> {
  const ctx = await pwRequest.newContext({ baseURL: USERS_URL });
  const res = await ctx.post("/api/v1/login", {
    data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD },
  });
  if (!res.ok()) {
    await ctx.dispose();
    throw new Error(`admin login failed: ${res.status()}`);
  }
  return ctx;
}

// Seed a visible static challenge through a dedicated admin API context, returning its id. This
// deliberately does not seed through the SPA-driven adminPage: the previous test leaves an async
// import in flight, and a browser session freshly minted alongside it can carry a mismatched CSRF
// token. An isolated request context has its own cookie jar and races nothing.
async function apiSeedChallenge(name: string): Promise<number> {
  const ctx = await freshAdmin();
  try {
    const me = (await (await ctx.get("/api/v1/me")).json()) as { csrf_token: string };
    const res = await ctx.post("/api/v1/admin/challenges", {
      headers: { "CSRF-Token": me.csrf_token },
      data: { name, category: "misc", value: 100, function: "static", state: "visible" },
    });
    if (!res.ok()) throw new Error(`seed ${name} failed: ${res.status()} ${await res.text()}`);
    return ((await res.json()) as { id: number }).id;
  } finally {
    await ctx.dispose();
  }
}

// The visible challenge names and whether a given id resolves, read through a fresh admin context so
// a restore that truncated sessions never leaves this read unauthenticated.
async function apiState(droppedId?: number): Promise<{ names: string[]; droppedStatus: number }> {
  const ctx = await freshAdmin();
  try {
    const res = await ctx.get("/api/v1/challenges?view=admin");
    const body = res.ok()
      ? ((await res.json()) as { challenges?: Array<{ name: string }> })
      : { challenges: [] };
    const names = (body.challenges ?? []).map((c) => c.name);
    const droppedStatus = droppedId ? (await ctx.get(`/api/v1/challenges/${droppedId}`)).status() : 0;
    return { names, droppedStatus };
  } finally {
    await ctx.dispose();
  }
}

// Lists visible challenge names through the admin board API, for out-of-band verification once the
// signed-in session used to launch a restore has been wiped by that restore.
async function challengeNames(page: Page): Promise<string[]> {
  return page.evaluate(async () => {
    const body = (await (
      await fetch("/api/v1/challenges?view=admin", { credentials: "include" })
    ).json()) as { challenges?: Array<{ name: string }> };
    return (body.challenges ?? []).map((c) => c.name);
  });
}

test.describe("ops — backup, restore and import", () => {
  test("a full backup completes and offers a download", async ({ adminPage }) => {
    await adminPage.goto("/admin");
    await adminPage.getByRole("button", { name: "Back up (full)" }).click();
    // The task settles to succeeded and hands back a real download link.
    await expect(adminPage.getByText("succeeded")).toBeVisible({ timeout: 30_000 });
    await expect(adminPage.getByRole("link", { name: "Download backup" })).toBeVisible();
  });

  test("a bad import is rejected loudly, not swallowed", async ({ adminPage }) => {
    await adminPage.goto("/admin");
    const bad = join(mkdtempSync(join(tmpdir(), "ff-import-")), "not-an-archive.zip");
    writeFileSync(bad, "this is plainly not a zip archive");

    // The import file input is the second hidden picker in the Backup & restore card.
    const card = adminPage.locator(".ff-card", { hasText: "Backup & restore" });
    await card.locator('input[type="file"]').nth(1).setInputFiles(bad);

    // Loud: a danger alert appears and no download is offered. The import must not report success.
    await expect(adminPage.locator(".ff-alert--danger")).toBeVisible({ timeout: 30_000 });
    await expect(adminPage.getByRole("link", { name: "Download backup" })).toHaveCount(0);
  });

  test("an export → restore round-trip reverts the instance", async ({ adminPage }) => {
    // Backup + async restore + repeated re-login can outrun the default per-test budget.
    test.setTimeout(150_000);
    // A marker that exists at backup time, and one created only afterwards.
    const keep = uniq("Keep");
    const dropped = uniq("Dropped");
    await apiSeedChallenge(keep);

    await adminPage.goto("/admin");
    await adminPage.getByRole("button", { name: "Back up (full)" }).click();
    const link = adminPage.getByRole("link", { name: "Download backup" });
    await expect(link).toBeVisible({ timeout: 30_000 });
    const href = await link.getAttribute("href");
    expect(href).not.toBeNull();

    // Pull the archive bytes down through the authenticated session and stash them.
    const resp = await adminPage.request.get(href!);
    expect(resp.ok()).toBeTruthy();
    const archive = join(mkdtempSync(join(tmpdir(), "ff-backup-")), "backup.zip");
    writeFileSync(archive, await resp.body());

    // Create the second marker AFTER the snapshot — it must not survive the restore.
    const droppedId = await apiSeedChallenge(dropped);
    expect(await challengeNames(adminPage)).toContain(dropped);

    // Restore the archive. This truncates and reloads every table — including sessions — so the
    // admin who launched it is logged out; the outcome is verified from a fresh session below.
    const card = adminPage.locator(".ff-card", { hasText: "Backup & restore" });
    await card.locator('input[type="file"]').nth(0).setInputFiles(archive);

    // Verify out of band, through a fresh admin API context each poll, until the instance has
    // reverted — the late marker gone AND the early one back. Two hazards to ride out: the restore is
    // asynchronous (a worker job), and it truncates every table, sessions included, before it reloads
    // them. A fresh context per read re-authenticates cleanly regardless, and waiting for the whole
    // terminal state avoids reading a half-truncated instance.
    await expect
      .poll(
        async () => {
          const { names } = await apiState();
          return names.includes(keep) && !names.includes(dropped);
        },
        { timeout: 90_000, intervals: [2_000] },
      )
      .toBe(true);
    const { names, droppedStatus } = await apiState(droppedId);
    expect(names).toContain(keep);
    expect(names).not.toContain(dropped);
    // The dropped challenge's detail is gone too.
    expect(droppedStatus).toBe(404);
  });
});
