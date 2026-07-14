import { useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import type { AdminBracket } from "../../../api/admin";
import { isApiError } from "../../../api/errors";
import { denialOf, PolicyGate } from "../../../policy";
import {
  adminBracketsQuery,
  adminUsersQuery,
  instanceQuery,
  useAssignBracket,
  useCreateBracket,
  useDeleteBracket,
  useUpdateBracket,
} from "../../../queries";
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDestructive,
  DataTable,
  Dialog,
  EmptyState,
  Field,
  fieldErrors,
  Form,
  Input,
  Select,
  useToast,
  type Column,
  type FieldErrors,
} from "../../../ui";

export const Route = createFileRoute("/_auth/admin/brackets")({
  component: BracketsPage,
});

type AccountMode = "users" | "teams";

function BracketsPage() {
  const toast = useToast();

  const instance = useQuery(instanceQuery);
  const mode = accountMode(instance.data);

  const brackets = useQuery(adminBracketsQuery);
  const rows = brackets.data?.brackets ?? [];

  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<AdminBracket | null>(null);
  const [deleting, setDeleting] = useState<AdminBracket | null>(null);

  const create = useCreateBracket();
  const update = useUpdateBracket();
  const remove = useDeleteBracket();

  const columns: readonly Column<AdminBracket>[] = [
    { key: "name", header: "name", cell: (b) => b.name },
    {
      key: "description",
      header: "description",
      cell: (b) =>
        b.description === undefined || b.description === "" ? (
          <span className="muted">—</span>
        ) : (
          <span className="ff-truncate">{b.description}</span>
        ),
    },
    {
      key: "applies_to",
      header: "applies to",
      cell: (b) => (
        <Badge tone={mode !== null && b.applies_to !== mode ? "warn" : "neutral"}>
          {b.applies_to}
        </Badge>
      ),
    },
    {
      key: "actions",
      header: "Actions",
      headerHidden: true,
      align: "right",
      cell: (b) => (
        <span className="ff-row">
          <Button size="sm" variant="ghost" onClick={() => setEditing(b)}>
            Edit
          </Button>
          <Button size="sm" variant="danger" onClick={() => setDeleting(b)}>
            Delete
          </Button>
        </span>
      ),
    },
  ];

  if (brackets.isError) {
    const denial = denialOf(brackets.error);
    if (denial !== null) return <PolicyGate error={brackets.error} />;
    return (
      <Alert tone="danger" title="Could not load brackets">
        <p>{messageOf(brackets.error)}</p>
        <Button size="sm" onClick={() => void brackets.refetch()}>
          Retry
        </Button>
      </Alert>
    );
  }

  const confirmDelete = () => {
    if (deleting === null) return;
    remove.mutate(deleting.id, {
      onSuccess: () => {
        const name = deleting.name;
        setDeleting(null);
        toast.success(`Deleted ${name}`, "Its members were unassigned, not deleted.");
      },
      onError: (error) => toast.error("Could not delete the bracket", messageOf(error)),
    });
  };

  return (
    <>
      <div className="page-head">
        <h1>brackets</h1>
        <Button variant="primary" onClick={() => setCreating(true)}>
          New bracket
        </Button>
      </div>

      <Card flush>
        <DataTable
          caption="Brackets"
          columns={columns}
          rows={rows}
          rowKey={(b) => b.id}
          loading={brackets.isPending}
          empty={
            <EmptyState
              title="No brackets yet"
              description="A bracket slices the scoreboard — a division, a school year, a track."
              action={
                <Button variant="primary" onClick={() => setCreating(true)}>
                  New bracket
                </Button>
              }
            />
          }
        />
      </Card>

      <AssignPanel mode={mode} brackets={rows} />

      {creating && (
        <BracketDialog
          mode={mode}
          busy={create.isPending}
          onClose={() => setCreating(false)}
          onSubmit={(body, onError) =>
            create.mutate(body, {
              onSuccess: (bracket) => {
                setCreating(false);
                toast.success(`Created ${bracket.name}`, `Applies to ${bracket.applies_to}.`);
              },
              onError,
            })
          }
        />
      )}

      {editing !== null && (
        <BracketDialog
          mode={mode}
          bracket={editing}
          busy={update.isPending}
          onClose={() => setEditing(null)}
          onSubmit={(body, onError) =>
            update.mutate(
              { id: editing.id, body: { name: body.name, description: body.description } },
              {
                onSuccess: (bracket) => {
                  setEditing(null);
                  toast.success(`Saved ${bracket.name}`);
                },
                onError,
              },
            )
          }
        />
      )}

      {deleting !== null && (
        <ConfirmDestructive
          open
          onClose={() => setDeleting(null)}
          onConfirm={confirmDelete}
          resourceName={deleting.name}
          resourceKind="bracket"
          busy={remove.isPending}
          description="Accounts in this bracket are unassigned — no account, solve or score is deleted."
        />
      )}
    </>
  );
}

interface BracketBody {
  name: string;
  description?: string;
  applies_to: AccountMode;
}

interface BracketDialogProps {
  mode: AccountMode | null;
  bracket?: AdminBracket;
  busy: boolean;
  onClose: () => void;
  onSubmit: (body: BracketBody, onError: (error: unknown) => void) => void;
}

function BracketDialog({ mode, bracket, busy, onClose, onSubmit }: BracketDialogProps) {
  const editing = bracket !== undefined;
  const [name, setName] = useState(bracket?.name ?? "");
  const [description, setDescription] = useState(bracket?.description ?? "");
  const [appliesTo, setAppliesTo] = useState<AccountMode>(bracket?.applies_to ?? mode ?? "users");
  const [errors, setErrors] = useState<FieldErrors>({});
  const [error, setError] = useState<string | null>(null);

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setErrors({});
    setError(null);
    onSubmit(
      {
        name: name.trim(),
        description: description.trim() === "" ? undefined : description.trim(),
        applies_to: appliesTo,
      },
      (failure) => {
        setErrors(fieldErrorsOf(failure));
        setError(messageOf(failure));
      },
    );
  };

  // An account can only ever be a user or a team — never both — so a bracket for the other
  // kind could match nothing. Offer only the mode this instance runs, and say why.
  const locked = mode !== null;
  const options =
    mode === null
      ? [
          { value: "users", label: "users" },
          { value: "teams", label: "teams" },
        ]
      : [{ value: mode, label: mode }];

  return (
    <Dialog
      open
      onClose={onClose}
      title={editing ? "Edit bracket" : "New bracket"}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" form="bracket-form" variant="primary" loading={busy}>
            {editing ? "Save" : "Create"}
          </Button>
        </>
      }
    >
      <Form id="bracket-form" onSubmit={submit} errors={errors} error={error}>
        <Field name="name" label="Name" required>
          <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={128} />
        </Field>

        <Field name="description" label="Description" hint="Shown on the public bracket filter.">
          <Input value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>

        <Field
          name="applies_to"
          label="Applies to"
          required
          hint={
            editing
              ? "Fixed at creation — a bracket cannot change what kind of account it holds."
              : locked
                ? `This instance's accounts are ${mode}, so a bracket can only apply to ${mode}.`
                : "The instance mode is unknown; pick the kind of account this instance scores."
          }
        >
          <Select
            value={appliesTo}
            disabled={editing || locked}
            options={options}
            onChange={(e) => setAppliesTo(e.target.value as AccountMode)}
          />
        </Field>
      </Form>
    </Dialog>
  );
}

/**
 * Assignment is two actions, not one control. `bracket_id: null` clears an account's
 * bracket, so a select that can be left blank would clear it by accident — the operator
 * has to ask for that.
 */
function AssignPanel({
  mode,
  brackets,
}: {
  mode: AccountMode | null;
  brackets: readonly AdminBracket[];
}) {
  const toast = useToast();
  const assign = useAssignBracket();

  const [accountId, setAccountId] = useState("");
  const [bracketId, setBracketId] = useState("");
  const [clearing, setClearing] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // In users mode an account *is* a user, so the admin user list is the picker. Teams have
  // no admin list endpoint, so the id is typed in.
  const pickFromUsers = mode === "users";
  const users = useQuery({ ...adminUsersQuery({ page: 1, per_page: 100 }), enabled: pickFromUsers });

  const eligible = brackets.filter((b) => mode === null || b.applies_to === mode);
  const id = Number(accountId);
  const account = Number.isSafeInteger(id) && id > 0 ? id : null;

  const write = (target: number | null, done: () => void) => {
    if (account === null) return;
    setError(null);
    assign.mutate(
      { accountId: account, bracketId: target },
      {
        onSuccess: (result) => {
          done();
          toast.success(
            result.bracket_id === null || result.bracket_id === undefined
              ? `${result.name} has no bracket`
              : `${result.name} is in ${nameOf(brackets, result.bracket_id)}`,
          );
        },
        onError: (failure) => setError(messageOf(failure)),
      },
    );
  };

  return (
    <Card
      title="Assign an account"
      footer={
        <span className="muted">
          Clearing removes the account from every bracket; it never removes the account.
        </span>
      }
    >
      {error !== null && (
        <Alert tone="danger" title="Refused">
          {error}
        </Alert>
      )}

      <div className="ff-stack">
        <Field
          name="account"
          label={mode === "teams" ? "Team account id" : "Account"}
          hint={
            mode === "teams"
              ? "Teams score as the account in this instance; paste the team's id."
              : undefined
          }
        >
          {pickFromUsers ? (
            <Select
              value={accountId}
              placeholder="Choose a user"
              onChange={(e) => setAccountId(e.target.value)}
              options={(users.data?.users ?? []).map((u) => ({
                value: String(u.id),
                label: `${u.name} · ${u.email}`,
              }))}
            />
          ) : (
            <Input
              value={accountId}
              inputMode="numeric"
              mono
              onChange={(e) => setAccountId(e.target.value)}
            />
          )}
        </Field>

        <Field
          name="bracket"
          label="Bracket"
          hint={
            eligible.length === 0
              ? "No bracket applies to this instance's accounts yet."
              : undefined
          }
        >
          <Select
            value={bracketId}
            placeholder="Choose a bracket"
            disabled={eligible.length === 0}
            onChange={(e) => setBracketId(e.target.value)}
            options={eligible.map((b) => ({ value: String(b.id), label: b.name }))}
          />
        </Field>

        <div className="ff-row">
          <Button
            variant="primary"
            loading={assign.isPending && !clearing}
            disabled={account === null || bracketId === ""}
            onClick={() => write(Number(bracketId), () => setBracketId(""))}
          >
            Assign
          </Button>
          <Button
            variant="danger"
            disabled={account === null}
            onClick={() => setClearing(true)}
          >
            Clear assignment
          </Button>
        </div>
      </div>

      <Dialog
        open={clearing}
        size="sm"
        onClose={() => setClearing(false)}
        title="Clear bracket assignment"
        description="The account leaves its bracket and only appears on the overall scoreboard."
        footer={
          <>
            <Button variant="ghost" onClick={() => setClearing(false)} disabled={assign.isPending}>
              Cancel
            </Button>
            <Button
              variant="danger"
              loading={assign.isPending && clearing}
              onClick={() => write(null, () => setClearing(false))}
            >
              Clear
            </Button>
          </>
        }
      />
    </Card>
  );
}

function nameOf(brackets: readonly AdminBracket[], id: number): string {
  return brackets.find((b) => b.id === id)?.name ?? `bracket ${id}`;
}

// `mode` ships on /instance but predates the checked-in schema, and a wrong guess here would
// offer a bracket that can never match an account. Unknown widens the choice; it never picks.
function accountMode(instance: unknown): AccountMode | null {
  const mode = (instance as { mode?: unknown } | undefined)?.mode;
  return mode === "users" || mode === "teams" ? mode : null;
}

function fieldErrorsOf(error: unknown): FieldErrors {
  return isApiError(error) ? fieldErrors({ errors: error.fieldErrors }) : {};
}

function messageOf(error: unknown): string {
  if (isApiError(error)) return error.detail;
  return error instanceof Error ? error.message : "unexpected error";
}
