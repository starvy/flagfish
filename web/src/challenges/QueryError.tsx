import { isApiError } from "../api/client";
import { PolicyGate, denialOf } from "../policy";
import { Alert, Button } from "../ui";

export interface QueryErrorProps {
  error: unknown;
  onRetry: () => void;
  title?: string;
}

/**
 * The error half of a screen's four states.
 *
 * A policy denial is not something a retry can fix, so it goes to the gate, which renders the
 * reason or follows the server's Location. Everything else is a fault worth asking about again,
 * and it is shown in the server's own words rather than as a status code.
 */
export function QueryError({ error, onRetry, title = "could not load" }: QueryErrorProps) {
  if (denialOf(error) !== null) return <PolicyGate error={error} />;

  return (
    <Alert tone="danger" title={title}>
      <p>{isApiError(error) ? error.detail : "the request failed before the server answered."}</p>
      <Button variant="secondary" size="sm" onClick={onRetry}>
        retry
      </Button>
    </Alert>
  );
}
</content>
