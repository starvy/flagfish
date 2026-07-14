import { Link, createFileRoute } from "@tanstack/react-router";
import { Alert, Card } from "../ui";

export const Route = createFileRoute("/setup")({
  component: SetupPage,
});

// Where the `setup-incomplete` denial lands. Setup is a CLI transaction — it creates the first
// admin and fixes the account model in the same commit — so there is no API to drive from here
// and this page does not pretend otherwise: it tells the operator what to run.
function SetupPage() {
  return (
    <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "44rem" }}>
      <Card title="This instance is not set up">
        <Alert tone="info" title="Setup runs on the server, not in the browser">
          Until it has run, every route — sign-in and registration included — is denied. There is
          no web form for this on purpose: the first admin and the account model are created in one
          transaction, by someone with a shell on the box.
        </Alert>

        <p>The operator runs, on the host:</p>

        <div className="ff-code">
          <div className="ff-code__head">
            <span>shell</span>
          </div>
          <pre className="ff-code__pre">
            <code>
              {"flagfish migrate\n" +
                "FLAGFISH_ADMIN_PASSWORD='…' flagfish admin create --email you@example.com --mode users\n" +
                "flagfish serve"}
            </code>
          </pre>
        </div>

        <p className="ff-muted">
          <code>admin create</code> is what completes setup: the same transaction marks the
          instance live and fixes the account model (<code>--mode users</code> or{" "}
          <code>--mode teams</code>), which cannot be changed afterwards. Re-running it is safe.
          Pass <code>--promote</code> to elevate an account that already exists. Prefer the
          environment variable over <code>--password</code>, which is visible in{" "}
          <code>ps</code> and in shell history.
        </p>

        <p className="ff-muted">
          Once it has run, <Link to="/login">sign in</Link>.
        </p>
      </Card>
    </div>
  );
}
