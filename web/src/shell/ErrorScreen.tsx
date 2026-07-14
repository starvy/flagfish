import { Link, useRouter } from "@tanstack/react-router";
import { denialOf, PolicyGate } from "../policy";
import { Alert, Button, EmptyState } from "../ui";

/**
 * The boundary under every route.
 *
 * A policy denial is an answer the server chose, and the policy gate renders it — a ban wall, a
 * countdown, a redirect. Anything else is a fault, and it is shown as one, with the message
 * intact: a crash dressed up as a polite notice is how a broken build ships.
 */
export function RouteError({ error }: { error: Error }) {
  const router = useRouter();

  if (denialOf(error) !== null) return <PolicyGate error={error} />;

  return (
    <main className="sh-screen" id="main">
      <Alert tone="danger" title="Something broke">
        {error.message || "The page failed to render."}
      </Alert>
      <div className="ff-row">
        <Button variant="secondary" onClick={() => void router.invalidate()}>
          retry
        </Button>
        <Button variant="ghost" onClick={() => location.reload()}>
          reload
        </Button>
      </div>
    </main>
  );
}

export function NotFoundScreen() {
  return (
    <main className="sh-screen" id="main">
      <EmptyState
        title="404 — no such page"
        description="The URL does not resolve to anything in this instance. It may exist only for an admin, or only in teams mode."
        action={
          <Link to="/challenges" className="ff-btn ff-btn--secondary">
            back to the challenges
          </Link>
        }
      />
    </main>
  );
}

export function PendingScreen() {
  return (
    <div className="sh-screen sh-pending" role="status" aria-live="polite">
      <span className="ff-spinner" aria-hidden="true" />
      <span className="ff-muted">loading…</span>
    </div>
  );
}
