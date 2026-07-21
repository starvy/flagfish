import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, createFileRoute, useRouter } from "@tanstack/react-router";
import { isApiError } from "../api/client";
import { PolicyGate, denialOf } from "../policy";
import { useConfirmEmailChange } from "../queries";
import { Alert, Button, Card, Field, Form, Input, Spinner } from "../ui";

export const Route = createFileRoute("/confirm-email")({
  validateSearch: (search: Record<string, unknown>): { token?: string } => ({
    token: typeof search.token === "string" && search.token !== "" ? search.token : undefined,
  }),
  component: ConfirmEmailPage,
});

function ConfirmEmailPage() {
  const { token } = Route.useSearch();
  return token === undefined ? <EnterCode /> : <ConfirmToken token={token} />;
}

function useNavigateOnSuccess(success: boolean) {
  const router = useRouter();
  useEffect(() => {
    if (success) void router.navigate({ to: "/settings", search: { tab: "profile" } });
  }, [success, router]);
}

function ConfirmToken({ token }: { token: string }) {
  const confirm = useConfirmEmailChange();
  const { mutate } = confirm;

  // Single-use: a second POST answers 400 and would turn a success on screen into a failure.
  // StrictMode re-runs effects, so the guard is not optional.
  const submitted = useRef(false);
  useEffect(() => {
    if (submitted.current) return;
    submitted.current = true;
    mutate({ token });
  }, [mutate, token]);

  useNavigateOnSuccess(confirm.isSuccess);

  const error: unknown = confirm.error;
  const deadLink = isApiError(error) && (error.status === 400 || error.status === 409);
  const inline = error === null || deadLink;
  const denial = inline ? null : denialOf(error);
  if (denial !== null) return <PolicyGate error={error} />;

  const message =
    error === null ? null : isApiError(error) ? error.detail : "The server could not be reached.";

  return (
    <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "30rem" }}>
      <Card title="Confirming your new email">
        {confirm.isPending && (
          <p className="ff-row ff-muted">
            <Spinner size="sm" label="Confirming" /> Checking the link…
          </p>
        )}
        {confirm.isSuccess && (
          <Alert tone="success" title="Your email is updated">
            Taking you back to settings.
          </Alert>
        )}
        {message !== null && (
          <>
            <Alert
              tone={deadLink ? "warn" : "danger"}
              title={deadLink ? "That link is no longer good" : "Could not confirm the change"}
            >
              {message}
            </Alert>
            <p className="ff-muted">
              Confirmation links expire. Start the change again from{" "}
              <Link to="/settings" search={{ tab: "profile" }}>
                your settings
              </Link>
              .
            </p>
          </>
        )}
      </Card>
    </div>
  );
}

// A code pasted from the email, for anyone who typed the address by hand instead of clicking through.
function EnterCode() {
  const confirm = useConfirmEmailChange();
  const [code, setCode] = useState("");
  useNavigateOnSuccess(confirm.isSuccess);

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const token = code.trim();
    if (token !== "") confirm.mutate({ token });
  };

  return (
    <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "30rem" }}>
      <Card title="Confirm your new email">
        <p>Paste the code from the confirmation email to finish changing your address.</p>
        <Form
          onSubmit={submit}
          error={confirm.error ? (isApiError(confirm.error) ? confirm.error.detail : "The server could not be reached.") : undefined}
          footer={
            <Button type="submit" variant="primary" loading={confirm.isPending} disabled={code.trim() === ""}>
              Confirm change
            </Button>
          }
        >
          <Field name="token" label="Confirmation code">
            <Input value={code} onChange={(e) => setCode(e.target.value)} autoComplete="one-time-code" required />
          </Field>
        </Form>
      </Card>
    </div>
  );
}
