import { useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import type { AdminAward } from "../api/admin";
import { adminAwardsQuery, useGrantAward, useRevokeAward } from "../queries";
import {
  Badge,
  Button,
  Card,
  Dialog,
  EmptyState,
  Field,
  Form,
  Input,
  RelativeTime,
  Skeleton,
  Textarea,
  useToast,
} from "../ui";
import { errorDetail } from "./errors";

/**
 * The manual-award surface: an out-of-band point adjustment (a cheating penalty, a live-dispute
 * correction) against one scoring account, plus a list of the adjustments already made with a
 * revoke control. accountId is a team id in teams mode and a user id in users mode — the server
 * resolves which from the instance, so the screen only needs the id and a label for its copy.
 */
export function AwardsPanel({
  accountId,
  accountKind,
}: {
  accountId: number;
  accountKind: "user" | "team";
}) {
  const awards = useQuery(adminAwardsQuery(accountId));

  return (
    <Card title="Points">
      <p className="muted">
        A manual adjustment moves this {accountKind}'s score out of band — a penalty for confirmed
        cheating, or a correction during a live dispute. Negative values subtract. Every grant and
        revoke is recorded in the audit trail with your name; a revoke is a real deletion, so the
        scoreboard reads as if the adjustment never happened.
      </p>

      <GrantForm accountId={accountId} accountKind={accountKind} />

      <div className="ff-stack" style={{ marginTop: "var(--space-4, 1rem)" }}>
        {awards.isPending ? (
          <Skeleton lines={3} />
        ) : awards.isError ? (
          <p className="muted">Could not load existing adjustments: {errorDetail(awards.error)}</p>
        ) : (awards.data.awards ?? []).length === 0 ? (
          <EmptyState title="No manual adjustments" description="Nothing has been granted or penalised by hand." />
        ) : (
          <ul className="ff-award-list">
            {(awards.data.awards ?? []).map((a) => (
              <AwardRow key={a.id} accountId={accountId} award={a} />
            ))}
          </ul>
        )}
      </div>
    </Card>
  );
}

function GrantForm({ accountId, accountKind }: { accountId: number; accountKind: "user" | "team" }) {
  const toast = useToast();
  const grant = useGrantAward(accountId);
  const [value, setValue] = useState("");
  const [reason, setReason] = useState("");

  const parsed = Number(value);
  // A nonzero integer and a reason: the same two rules the server enforces, echoed so the button
  // reads as disabled rather than the submit bouncing back a 422.
  const valid = Number.isInteger(parsed) && parsed !== 0 && reason.trim() !== "";

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!valid) return;
    grant.mutate(
      { value: parsed, reason: reason.trim() },
      {
        onSuccess: (a) => {
          setValue("");
          setReason("");
          toast.success(
            a.value >= 0 ? `Granted ${a.value} points` : `Penalised ${Math.abs(a.value)} points`,
          );
        },
        onError: (error) => toast.error("Could not apply the adjustment", errorDetail(error)),
      },
    );
  };

  return (
    <Form
      onSubmit={submit}
      error={grant.error ? errorDetail(grant.error) : undefined}
      footer={
        <Button type="submit" variant="primary" loading={grant.isPending} disabled={!valid}>
          Apply adjustment
        </Button>
      }
    >
      <Field name="value" label="Points" hint="Whole number; negative to penalise." required>
        <Input
          type="number"
          inputMode="numeric"
          step={1}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder="e.g. -250"
        />
      </Field>
      <Field name="reason" label="Reason" hint={`Recorded in the audit trail for this ${accountKind}.`} required>
        <Textarea
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          maxLength={500}
          rows={2}
          placeholder="flag-sharing penalty, ticket #42"
        />
      </Field>
    </Form>
  );
}

function AwardRow({ accountId, award }: { accountId: number; award: AdminAward }) {
  const toast = useToast();
  const revoke = useRevokeAward(accountId);
  const [confirm, setConfirm] = useState(false);

  return (
    <li className="ff-award-row">
      <span className="ff-row">
        <Badge tone={award.value >= 0 ? "success" : "danger"}>
          {award.value >= 0 ? `+${award.value}` : award.value}
        </Badge>
        <span className="ff-truncate">{award.reason}</span>
      </span>
      <span className="ff-row">
        <span className="muted">
          <RelativeTime value={award.date} />
        </span>
        <Button size="sm" variant="ghost" onClick={() => setConfirm(true)}>
          Revoke
        </Button>
      </span>

      {confirm && (
        <Dialog
          open
          size="sm"
          onClose={() => setConfirm(false)}
          title="Revoke adjustment"
          description="The adjustment is deleted from the ledger. The scoreboard moves as if it never happened; stamped solves are untouched."
          footer={
            <>
              <Button variant="ghost" onClick={() => setConfirm(false)} disabled={revoke.isPending}>
                Cancel
              </Button>
              <Button
                variant="danger"
                loading={revoke.isPending}
                onClick={() =>
                  revoke.mutate(award.id, {
                    onSuccess: () => {
                      setConfirm(false);
                      toast.success("Adjustment revoked");
                    },
                    onError: (error) => toast.error("Could not revoke", errorDetail(error)),
                  })
                }
              >
                Revoke
              </Button>
            </>
          }
        />
      )}
    </li>
  );
}
