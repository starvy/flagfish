import { Client } from "pg";
import { test, expect } from "../fixtures";

// The anti-cheat screens are where a human triages a cheating accusation, so they must name *who* is
// involved — not just a bare account id. This seeds a flag-sharing pair directly in Postgres (two
// named accounts, a flag issued to one and submitted by the other) and asserts the reviewer sees the
// display names, not `#2` / `#3`. Before the read-projection fix the queries selected only ids and
// this fails: the names never reach the page.
//
// The DSN comes from the environment so the spec runs against whatever instance the coordinator
// booted; the default matches the local bring-up in the task.
const DSN =
  process.env.FLAGFISH_E2E_DATABASE_URL ??
  "postgres://flagfish:flagfish@localhost:5584/flagfish_e2e?sslmode=disable";

interface SeedResult {
  issuer: string;
  submitter: string;
}

async function seedSharingPair(): Promise<SeedResult> {
  const stamp = Date.now();
  const issuer = `Issuer_${stamp}`;
  const submitter = `Submitter_${stamp}`;
  const client = new Client({ connectionString: DSN });
  await client.connect();
  try {
    const chal = await client.query<{ id: string }>(
      `INSERT INTO challenges (name, category, value, flag_mode)
       VALUES ($1, 'misc', 100, 'unique') RETURNING id`,
      [`ac-e2e-${stamp}`],
    );
    const challengeID = chal.rows[0].id;

    const mk = async (name: string) => {
      const r = await client.query<{ id: string }>(
        `INSERT INTO users (name, email) VALUES ($1, $2) RETURNING id`,
        [name, `${name}@e2e.test`],
      );
      return r.rows[0].id;
    };
    const issuerID = await mk(issuer);
    const submitterID = await mk(submitter);

    // The submitter answers correctly with a flag attributed to the issuer's account: cross-account
    // sharing the detector fires on. Two rows so the pair is unambiguous.
    for (let i = 0; i < 2; i++) {
      await client.query(
        `INSERT INTO submissions (challenge_id, user_id, type, provided, ip, attributed_account_id)
         VALUES ($1, $2, 'correct', 'x', '203.0.113.9', $3)`,
        [challengeID, submitterID, issuerID],
      );
    }
    return { issuer, submitter };
  } finally {
    await client.end();
  }
}

test("anti-cheat flag-sharing names the accounts, not just their ids", async ({
  adminPage: page,
}) => {
  const { issuer, submitter } = await seedSharingPair();

  await page.goto("/admin/anticheat");
  await expect(page.getByRole("heading", { name: "anticheat" })).toBeVisible();

  // The Flag-sharing tab is the default; the seeded pair renders both display names.
  await expect(page.getByText(issuer, { exact: false }).first()).toBeVisible();
  await expect(page.getByText(submitter, { exact: false }).first()).toBeVisible();
});
