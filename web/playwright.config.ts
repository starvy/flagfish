import { defineConfig, devices } from "@playwright/test";

// Two real instances are booted out of band by web/e2e/scripts/up.sh: a teams-mode instance on 8019
// and a users-mode instance on 8020. Each project drives a browser at one of them. The account model
// is fixed at setup, so a spec belongs to exactly one project.
const TEAMS_URL = process.env.FLAGFISH_TEAMS_URL ?? "http://localhost:8019";
const USERS_URL = process.env.FLAGFISH_USERS_URL ?? "http://localhost:8020";

// Specs that require a users-mode instance (no teams). Everything else runs against teams.
const USERS_SPECS = [
  // The anti-cheat seed is account-based (bare users, attributed by user id); the detector resolves
  // names by the account unit, so it only reads back names in users mode.
  "admin-anticheat-names.spec.ts",
  "users-mode-core.spec.ts",
  "gameplay-flag-types.spec.ts",
  "gameplay-attempts-throttle.spec.ts",
  "gameplay-prerequisites.spec.ts",
  "gameplay-hints.spec.ts",
  "ops-pause.spec.ts",
  "ops-csv-export.spec.ts",
  "ops-audit-log.spec.ts",
  "ops-restore-import.spec.ts",
  "content-tags.spec.ts",
  "scoreboard-time-travel.spec.ts",
];

// Out-of-band DB access. Email-token reads (db.ts) run against the teams instance; the anti-cheat
// seed runs against the users instance. Each module reads the DSN for its own instance.
process.env.FLAGFISH_E2E_DATABASE_URL ??=
  "postgres://flagfish:flagfish@localhost:5588/flagfish?sslmode=disable";
process.env.FLAGFISH_USERS_DATABASE_URL ??=
  "postgres://flagfish:flagfish@localhost:5589/flagfish?sslmode=disable";

export default defineConfig({
  testDir: "./e2e/specs",
  globalSetup: "./e2e/global-setup.ts",
  // Specs share a single server's state (pause, restore, the clock, uniquely-named seed data), so the
  // whole suite runs in one worker. Parallelism here would mean two specs racing the same database.
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 120_000,
  expect: { timeout: 15_000 },
  reporter: [["list"]],
  use: {
    headless: true,
    trace: "retain-on-failure",
    acceptDownloads: true,
    ...devices["Desktop Chrome"],
  },
  projects: [
    {
      name: "teams",
      testIgnore: USERS_SPECS,
      use: { baseURL: TEAMS_URL },
    },
    {
      name: "users",
      testMatch: USERS_SPECS,
      use: { baseURL: USERS_URL },
    },
  ],
});
