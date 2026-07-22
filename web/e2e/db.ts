import { Client } from "pg";

// Out-of-band DB reads for token flows. No mailserver runs in the test stack, and flagfish never
// stores a reset/verify/email-change token in plaintext — email_tokens holds only sha256(token).
// The one place the plaintext exists is the queued email itself: the send job is enqueued in the
// same transaction as the token row, and its rendered body carries the code. With no SMTP the send
// stays retryable, so the row (and its args) sit in river_job for the test to read. Reading it here
// is legitimate setup — every assertion still goes through the browser. The token flows are teams
// mode, so this points at the teams instance's database.
const connectionString =
  process.env.FLAGFISH_E2E_DATABASE_URL ??
  process.env.FLAGFISH_DATABASE_URL ??
  "postgres://flagfish:flagfish@localhost:5588/flagfish?sslmode=disable";

async function withClient<T>(fn: (c: Client) => Promise<T>): Promise<T> {
  const client = new Client({ connectionString });
  await client.connect();
  try {
    return await fn(client);
  } finally {
    await client.end();
  }
}

// The 64-hex code flagfish mails for verify / reset / email-change tokens.
const TOKEN_RE = /[0-9a-f]{64}/;

/**
 * The plaintext token from the most recent email queued to `recipient`.
 *
 * `recipient` is the address the mail was sent TO — for a reset that is the account's address, for
 * an email change it is the NEW address the code proves control of. Polls briefly because the send
 * job is enqueued asynchronously after the browser's request returns.
 */
export async function latestEmailToken(recipient: string): Promise<string> {
  for (let attempt = 0; attempt < 20; attempt++) {
    const token = await withClient(async (c) => {
      const res = await c.query<{ body: string }>(
        `SELECT args->>'body' AS body
           FROM river_job
          WHERE kind = 'email_send' AND args->>'to' = $1
          ORDER BY id DESC
          LIMIT 1`,
        [recipient],
      );
      const body = res.rows[0]?.body;
      return body ? (TOKEN_RE.exec(body)?.[0] ?? null) : null;
    });
    if (token !== null) return token;
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`no queued email with a token found for ${recipient}`);
}

/** Whether an account's live (verified) email currently equals `email`. */
export async function liveEmailOf(userId: number): Promise<string> {
  return withClient(async (c) => {
    const res = await c.query<{ email: string }>(`SELECT email FROM users WHERE id = $1`, [userId]);
    if (res.rows.length === 0) throw new Error(`no user ${userId}`);
    return res.rows[0].email;
  });
}
