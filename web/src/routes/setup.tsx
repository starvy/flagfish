import { Link, createFileRoute } from "@tanstack/react-router";
import { Alert, Card, CodeBlock } from "../ui";

export const Route = createFileRoute("/setup")({
  component: SetupPage,
});

const BOOTSTRAP = `flagfish migrate
FLAGFISH_ADMIN_PASSWORD='…' flagfish admin create --email you@example.com --mode users
flagfish serve`;

// Where the `setup-incomplete` denial lands. Setup is a CLI transaction — it creates the first
// admin and fixes the account model in the same commit — and there is no API behind it, so this
// page tells the operator what to run instead of pretending to be a form.
function SetupPage() {
  return (
    <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "44rem" }}>
      <Card title="This instance is not set up">
        <Alert tone="info" title="Setup runs on the server, not in the browser">
          Until it has run, every route — sign-in and registration included — is denied. There is no
          web form for this on purpose: the first admin and the account model are created in one
          transaction, by someone with a shell on the host.
        </Alert>

        <p>The operator runs:</p>

        <CodeBlock code={BOOTSTRAP} language="shell" />

        <p className="ff-muted">
          <code>admin create</code> is the step that completes setup: the same transaction marks the
          instance live and fixes the account model — <code>--mode users</code> or{" "}
          <code>--mode teams</code> — which cannot be changed afterwards. Re-running it is safe, and{" "}
          <code>--promote</code> elevates an account that already exists. Prefer the environment
          variable to <code>--password</code>, which is visible in <code>ps</code> and in shell
          history.
        </p>

        <p className="ff-muted">
          Once it has run, <Link to="/login">sign in</Link>.
        </p>
      </Card>
    </div>
  );
}
