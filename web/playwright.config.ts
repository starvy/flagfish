import { defineConfig, devices } from "@playwright/test";

// The server under test is booted out of band (real Postgres + MinIO); the harness only drives a
// browser at it. baseURL comes from the environment so CI and a local run point at the same spec.
export default defineConfig({
  testDir: "./e2e/specs",
  fullyParallel: false,
  workers: 1,
  timeout: 45_000,
  expect: { timeout: 10_000 },
  reporter: [["list"]],
  use: {
    baseURL: process.env.FLAGFISH_E2E_URL ?? "http://localhost:8014",
    headless: true,
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
