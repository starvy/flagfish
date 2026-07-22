import { chromium, type FullConfig } from "@playwright/test";

// The teams-mode specs `challenge-play-ux` and `profile-team-display` drive a fixed seed: challenge
// #1, a file-less static challenge worth 250 that takes flag{e2e_win} and carries one 50-point hint.
// A fresh, migrated database has no challenges, so the first one created here takes id 1. Seeded once,
// before any spec runs, through the same admin API the editor uses. Idempotent: a re-run against an
// already-seeded instance leaves it untouched.
const TEAMS_URL = process.env.FLAGFISH_TEAMS_URL ?? "http://localhost:8019";
const ADMIN_EMAIL = process.env.FLAGFISH_E2E_ADMIN_EMAIL ?? "admin@example.com";
const ADMIN_PASSWORD = process.env.FLAGFISH_E2E_ADMIN_PASSWORD ?? "AdminPass123!";

const FLAG = "flag{e2e_win}";
const HINT_BODY = "The answer is hex, not decimal.";

export default async function globalSetup(_config: FullConfig): Promise<void> {
  const browser = await chromium.launch();
  const context = await browser.newContext({ baseURL: TEAMS_URL });
  const page = await context.newPage();
  try {
    await page.goto("/login");
    await page.getByLabel("Email").fill(ADMIN_EMAIL);
    await page.getByLabel("Password").fill(ADMIN_PASSWORD);
    await page.getByRole("button", { name: "Sign in" }).click();
    await page.waitForURL((url) => !url.pathname.startsWith("/login"));

    const result = await page.evaluate(
      async ({ flag, hintBody }) => {
        const me = (await (await fetch("/api/v1/me", { credentials: "include" })).json()) as {
          csrf_token: string;
        };
        const call = async (method: string, path: string, body?: unknown) => {
          const res = await fetch(`/api/v1${path}`, {
            method,
            credentials: "include",
            headers: { "Content-Type": "application/json", "CSRF-Token": me.csrf_token },
            body: body === undefined ? undefined : JSON.stringify(body),
          });
          if (!res.ok) throw new Error(`${method} ${path} -> ${res.status}: ${await res.text()}`);
          return res.status === 204 ? null : res.json();
        };

        // Idempotent: challenge #1 is the seed. Probe it by id (the list endpoint paginates, so a
        // populated instance may not return it on the first page). If it exists, leave everything be.
        const probe = await fetch("/api/v1/admin/challenges/1", {
          credentials: "include",
          headers: { "CSRF-Token": me.csrf_token },
        });
        if (probe.ok) return { seeded: false, id: 1 };

        const chal = (await call("POST", "/admin/challenges", {
          name: "Warmup",
          category: "misc",
          value: 250,
          function: "static",
          state: "visible",
        })) as { id: number };
        await call("POST", `/admin/challenges/${chal.id}/flags`, {
          type: "static",
          content: flag,
          case_insensitive: false,
        });
        await call("POST", `/admin/challenges/${chal.id}/hints`, {
          title: "Hint",
          content: hintBody,
          cost: 50,
          prerequisites: [],
        });
        return { seeded: true, id: chal.id };
      },
      { flag: FLAG, hintBody: HINT_BODY },
    );

    if (result.id !== 1) {
      throw new Error(
        `seed challenge must be id 1 (the teams specs hardcode /challenges/1) but got ${result.id}; ` +
          `boot a fresh teams instance with web/e2e/scripts/up.sh`,
      );
    }
  } finally {
    await context.close();
    await browser.close();
  }
}
