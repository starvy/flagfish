import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { isApiError, type ChallengeDetail } from "../../../api/client";
import { adminApi, type AdminChallenge, type AdminFlag } from "../../../api/admin";
import {
  challengeQuery,
  useAddFlag,
  useAddHint,
  useCreateChallenge,
  useDeleteFile,
  useDeleteFlag,
  useDeleteHint,
  useSetChallengeState,
  useUpdateChallenge,
  useUpdateFlag,
  useUpdateHint,
  useUploadFile,
} from "../../../queries";
import { PolicyGate, denialOf } from "../../../policy";
import {
  Alert,
  Badge,
  Button,
  Card,
  Checkbox,
  ConfirmDestructive,
  DataTable,
  Dialog,
  EmptyState,
  Field,
  fieldErrors,
  Form,
  Input,
  Markdown,
  Select,
  Skeleton,
  Tabs,
  Textarea,
  useToast,
  type Column,
  type FieldErrors,
} from "../../../ui";

/** The id that means "this challenge does not exist yet"; the Details tab creates it. */
const NEW = "new";

export const Route = createFileRoute("/_auth/admin/challenges/$challengeId")({
  component: ChallengeEditor,
});

type ChallengePatch = Parameters<typeof adminApi.updateChallenge>[1];
type FlagType = "static" | "regex";

function ChallengeEditor() {
  const { challengeId } = Route.useParams();
  const isNew = challengeId === NEW;
  const parsed = Number(challengeId);
  const id = isNew || !Number.isInteger(parsed) ? null : parsed;

  // The last body the server wrote back. It outranks the read: it is newer, and it carries the
  // fields the player-facing read does not return (logic, position, the decay parameters).
  const [saved, setSaved] = useState<AdminChallenge | null>(null);

  const detail = useQuery({ ...challengeQuery(id ?? 0), enabled: id !== null, retry: false });

  if (!isNew && id === null) {
    return <Alert tone="danger" title="Not a challenge id">{challengeId} is not a number.</Alert>;
  }

  if (id !== null && detail.isPending) {
    return (
      <div className="ff-stack">
        <Skeleton height="2rem" width="16rem" />
        <Skeleton lines={6} height="1.5rem" />
      </div>
    );
  }

  if (id !== null && detail.isError && saved === null) {
    // A hidden challenge is a 404 on every read the API publishes — the board and the detail are
    // both visible-only. Publishing it is the only way back to a body, and it is one op away.
    if (isApiError(detail.error) && detail.error.status === 404) {
      return <UnreadableChallenge id={id} onPublished={setSaved} />;
    }
    return denialOf(detail.error) ? (
      <PolicyGate error={detail.error} />
    ) : (
      <Alert tone="danger" title="Could not load the challenge">
        <p>{messageOf(detail.error)}</p>
        <Button onClick={() => void detail.refetch()}>Retry</Button>
      </Alert>
    );
  }

  const read = detail.data ?? null;
  const state = saved?.state ?? read?.state ?? "visible";
  const name = saved?.name ?? read?.name ?? "New challenge";

  return (
    <div className="ff-stack">
      <div className="page-head">
        <div className="ff-row">
          <h1>{name}</h1>
          {!isNew &&
            (state === "visible" ? (
              <Badge tone="success">published</Badge>
            ) : (
              <Badge tone="neutral">hidden</Badge>
            ))}
        </div>
        <Link to="/admin/challenges">
          <Button variant="ghost">Back to challenges</Button>
        </Link>
      </div>

      <Tabs
        label="Challenge editor"
        items={[
          {
            id: "details",
            label: "Details",
            content: <DetailsTab id={id} read={read} saved={saved} onSaved={setSaved} />,
          },
          {
            id: "flags",
            label: "Flags",
            disabled: id === null,
            content: id === null ? null : <FlagsTab challengeId={id} />,
          },
          {
            id: "hints",
            label: "Hints",
            disabled: id === null,
            content: id === null || read === null ? null : <HintsTab challengeId={id} read={read} />,
          },
          {
            id: "files",
            label: "Files",
            disabled: id === null,
            content: id === null || read === null ? null : <FilesTab read={read} />,
          },
        ]}
      />
    </div>
  );
}

function UnreadableChallenge({
  id,
  onPublished,
}: {
  id: number;
  onPublished: (ch: AdminChallenge) => void;
}) {
  const setState = useSetChallengeState();
  const toast = useToast();

  const publish = async () => {
    try {
      const ch = await setState.mutateAsync({ id, state: "visible" });
      onPublished(ch);
      toast.success("Published", ch.name);
    } catch (e) {
      toast.error("Could not publish", messageOf(e));
    }
  };

  return (
    <Alert tone="warn" title="Nothing to read here">
      <p>
        Challenge {id} is either hidden or gone. The API only reads out visible challenges, so a
        hidden one cannot be loaded — publishing it brings it back.
      </p>
      <Button variant="primary" loading={setState.isPending} onClick={() => void publish()}>
        Publish challenge {id}
      </Button>
    </Alert>
  );
}

/* ---------------------------------------------------------------- details */

interface FormState {
  name: string;
  category: string;
  description: string;
  attribution: string;
  connection_info: string;
  state: "visible" | "hidden";
  function: "static" | "linear" | "logarithmic";
  logic: "any" | "all" | "";
  value: string;
  initial: string;
  minimum: string;
  decay: string;
  max_attempts: string;
  position: string;
}

const BLANK: FormState = {
  name: "",
  category: "",
  description: "",
  attribution: "",
  connection_info: "",
  state: "visible",
  function: "static",
  logic: "any",
  value: "0",
  initial: "",
  minimum: "",
  decay: "",
  max_attempts: "0",
  position: "",
};

// The read the console has is the player's: it carries no logic, no position and no decay
// parameters. They seed blank, stay blank until the operator types one, and a field the operator
// never touched is omitted from the PATCH — which is exactly the server's "omit keeps" rule.
function seedOf(ch: AdminChallenge | null, read: ChallengeDetail | null): FormState {
  if (ch) {
    return {
      name: ch.name,
      category: ch.category,
      description: ch.description ?? "",
      attribution: ch.attribution ?? "",
      connection_info: ch.connection_info ?? "",
      state: ch.state,
      function: ch.function,
      logic: ch.logic,
      value: String(ch.value),
      initial: optNum(ch.initial),
      minimum: optNum(ch.minimum),
      decay: optNum(ch.decay),
      max_attempts: String(ch.max_attempts),
      position: String(ch.position),
    };
  }
  if (read) {
    return {
      ...BLANK,
      name: read.name,
      category: read.category,
      description: read.description ?? "",
      attribution: read.attribution ?? "",
      connection_info: read.connection_info ?? "",
      state: read.state,
      function: read.function,
      logic: "",
      value: String(read.value),
      max_attempts: String(read.max_attempts),
    };
  }
  return BLANK;
}

function DetailsTab({
  id,
  read,
  saved,
  onSaved,
}: {
  id: number | null;
  read: ChallengeDetail | null;
  saved: AdminChallenge | null;
  onSaved: (ch: AdminChallenge) => void;
}) {
  const navigate = useNavigate();
  const toast = useToast();
  const create = useCreateChallenge();
  const update = useUpdateChallenge();
  const setState = useSetChallengeState();

  const [base, setBase] = useState<FormState>(() => seedOf(saved, read));
  const [form, setForm] = useState<FormState>(base);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [failure, setFailure] = useState<string | null>(null);

  const set = <K extends keyof FormState>(key: K, value: FormState[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  const accept = (ch: AdminChallenge) => {
    const next = seedOf(ch, null);
    setBase(next);
    setForm(next);
    onSaved(ch);
  };

  const busy = create.isPending || update.isPending || setState.isPending;
  const decayed = form.function !== "static";

  const submit = async () => {
    const local = validate(form, id === null);
    setErrors(local);
    setFailure(null);
    if (Object.keys(local).length > 0) return;

    try {
      if (id === null) {
        const ch = await create.mutateAsync({
          name: form.name,
          category: form.category,
          state: form.state,
          value: Number(form.value),
          function: form.function,
          logic: form.logic === "" ? "any" : form.logic,
          max_attempts: Number(form.max_attempts || 0),
          ...(form.description === "" ? {} : { description: form.description }),
          ...(form.attribution === "" ? {} : { attribution: form.attribution }),
          ...(form.connection_info === "" ? {} : { connection_info: form.connection_info }),
          ...(form.initial === "" ? {} : { initial: Number(form.initial) }),
          ...(form.minimum === "" ? {} : { minimum: Number(form.minimum) }),
          ...(form.decay === "" ? {} : { decay: Number(form.decay) }),
          ...(form.position === "" ? {} : { position: Number(form.position) }),
        });
        accept(ch);
        toast.success("Challenge created", ch.name);
        void navigate({
          to: "/admin/challenges/$challengeId",
          params: { challengeId: String(ch.id) },
          replace: true,
        });
        return;
      }

      const patch = patchOf(form, base);
      const stateChanged = form.state !== base.state;

      if (Object.keys(patch).length === 0 && !stateChanged) {
        toast.info("Nothing to save");
        return;
      }

      let ch = saved;
      if (Object.keys(patch).length > 0) ch = await update.mutateAsync({ id, body: patch });
      // State is its own operation, so the last write back is the one that carries the truth.
      if (stateChanged) ch = await setState.mutateAsync({ id, state: form.state });
      if (ch) accept(ch);
      toast.success("Challenge saved", ch?.name ?? form.name);
    } catch (e) {
      if (isApiError(e)) {
        setErrors(fieldErrors({ errors: e.fieldErrors }));
        setFailure(e.detail);
        return;
      }
      setFailure(messageOf(e));
    }
  };

  return (
    <div className="ff-stack">
      <Form
        errors={errors}
        error={failure}
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
        footer={
          <Button type="submit" variant="primary" loading={busy}>
            {id === null ? "Create challenge" : "Save"}
          </Button>
        }
      >
        <Field name="name" label="Name" required>
          <Input value={form.name} onChange={(e) => set("name", e.target.value)} />
        </Field>

        <Field name="category" label="Category" required>
          <Input value={form.category} onChange={(e) => set("category", e.target.value)} />
        </Field>

        <Field name="state" label="State" hint="A hidden challenge is off the board for players.">
          <Select
            value={form.state}
            onChange={(e) => set("state", e.target.value as FormState["state"])}
            options={[
              { value: "visible", label: "Published" },
              { value: "hidden", label: "Hidden" },
            ]}
          />
        </Field>

        <Field name="description" label="Description" hint="Markdown. No raw HTML.">
          <Textarea
            value={form.description}
            onChange={(e) => set("description", e.target.value)}
            rows={10}
          />
        </Field>

        <Card title="Preview">
          {form.description === "" ? (
            <p className="ff-muted">The description renders here as the player sees it.</p>
          ) : (
            <Markdown source={form.description} />
          )}
        </Card>

        <Field name="attribution" label="Attribution" hint="Blank clears it.">
          <Input value={form.attribution} onChange={(e) => set("attribution", e.target.value)} />
        </Field>

        <Field name="connection_info" label="Connection info" hint="Blank clears it.">
          <Input
            mono
            value={form.connection_info}
            onChange={(e) => set("connection_info", e.target.value)}
          />
        </Field>

        <Field name="value" label="Value" required hint="Points, or the starting points when scoring decays.">
          <Input
            type="number"
            min={0}
            value={form.value}
            onChange={(e) => set("value", e.target.value)}
          />
        </Field>

        <Field name="function" label="Scoring">
          <Select
            value={form.function}
            onChange={(e) => set("function", e.target.value as FormState["function"])}
            options={[
              { value: "static", label: "static — always worth its value" },
              { value: "linear", label: "linear decay" },
              { value: "logarithmic", label: "logarithmic decay" },
            ]}
          />
        </Field>

        {decayed && (
          <>
            <Field
              name="initial"
              label="Initial"
              hint="Decay needs initial, minimum and decay together. Blank keeps what is stored."
            >
              <Input
                type="number"
                min={0}
                value={form.initial}
                onChange={(e) => set("initial", e.target.value)}
              />
            </Field>
            <Field name="minimum" label="Minimum" hint="The floor the value decays to.">
              <Input
                type="number"
                min={0}
                value={form.minimum}
                onChange={(e) => set("minimum", e.target.value)}
              />
            </Field>
            <Field name="decay" label="Decay" hint="Solves it takes to reach the minimum.">
              <Input
                type="number"
                min={1}
                value={form.decay}
                onChange={(e) => set("decay", e.target.value)}
              />
            </Field>
          </>
        )}

        <Field name="max_attempts" label="Max attempts" hint="0 is unlimited.">
          <Input
            type="number"
            min={0}
            value={form.max_attempts}
            onChange={(e) => set("max_attempts", e.target.value)}
          />
        </Field>

        <Field
          name="logic"
          label="Flag logic"
          hint="any: one flag judges it correct. all: every flag must match."
        >
          <Select
            value={form.logic}
            onChange={(e) => set("logic", e.target.value as FormState["logic"])}
            options={[
              ...(form.logic === "" ? [{ value: "", label: "unchanged" }] : []),
              { value: "any", label: "any" },
              { value: "all", label: "all" },
            ]}
          />
        </Field>

        <Field name="position" label="Board position" hint="Blank keeps it. The list orders by drag.">
          <Input
            type="number"
            value={form.position}
            onChange={(e) => set("position", e.target.value)}
          />
        </Field>
      </Form>
    </div>
  );
}

function validate(form: FormState, creating: boolean): FieldErrors {
  const out: FieldErrors = {};
  if (form.name.trim() === "") out.name = "A challenge needs a name.";
  if (form.category.trim() === "") out.category = "A challenge needs a category.";
  if (!isCount(form.value)) out.value = "A whole number of points, zero or more.";
  if (form.max_attempts !== "" && !isCount(form.max_attempts)) {
    out.max_attempts = "A whole number, zero or more. 0 is unlimited.";
  }
  if (creating && form.function !== "static") {
    for (const key of ["initial", "minimum", "decay"] as const) {
      if (form[key] === "") out[key] = "Decayed scoring needs initial, minimum and decay.";
    }
  }
  return out;
}

/** The three-state PATCH: an untouched field is omitted (keep), a cleared one is null. */
function patchOf(form: FormState, base: FormState): ChallengePatch {
  const patch: ChallengePatch = {};
  if (form.name !== base.name) patch.name = form.name;
  if (form.category !== base.category) patch.category = form.category;
  if (form.description !== base.description) patch.description = form.description;
  if (form.value !== base.value) patch.value = Number(form.value);
  if (form.function !== base.function) patch.function = form.function;
  if (form.max_attempts !== base.max_attempts) patch.max_attempts = Number(form.max_attempts || 0);
  if (form.logic !== base.logic && form.logic !== "") patch.logic = form.logic;
  if (form.position !== base.position && form.position !== "") patch.position = Number(form.position);
  if (form.attribution !== base.attribution) patch.attribution = orNull(form.attribution);
  if (form.connection_info !== base.connection_info) {
    patch.connection_info = orNull(form.connection_info);
  }
  if (form.initial !== base.initial) patch.initial = numOrNull(form.initial);
  if (form.minimum !== base.minimum) patch.minimum = numOrNull(form.minimum);
  if (form.decay !== base.decay) patch.decay = numOrNull(form.decay);
  return patch;
}

/* ------------------------------------------------------------------ flags */

function FlagsTab({ challengeId }: { challengeId: number }) {
  const toast = useToast();
  const add = useAddFlag();
  const update = useUpdateFlag();
  const remove = useDeleteFlag();

  // There is no read operation for flags anywhere on the API, so this is the only honest list:
  // the bodies the server wrote back to us. Reloading the page starts it empty again.
  const [flags, setFlags] = useState<AdminFlag[]>([]);

  const [type, setType] = useState<FlagType>("static");
  const [content, setContent] = useState("");
  const [insensitive, setInsensitive] = useState(false);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [failure, setFailure] = useState<string | null>(null);

  const [editing, setEditing] = useState<AdminFlag | null>(null);
  const [target, setTarget] = useState<AdminFlag | null>(null);

  const submit = async () => {
    setFailure(null);
    const bad = flagProblem(type, content);
    setErrors(bad === null ? {} : { content: bad });
    if (bad !== null) return;

    try {
      const flag = await add.mutateAsync({
        challengeId,
        body: { type, content, case_insensitive: insensitive },
      });
      setFlags((list) => [...list, flag]);
      setContent("");
      setInsensitive(false);
      toast.success("Flag added");
    } catch (e) {
      if (isApiError(e)) {
        setErrors(fieldErrors({ errors: e.fieldErrors }));
        setFailure(e.detail);
        return;
      }
      setFailure(messageOf(e));
    }
  };

  const confirmDelete = async () => {
    if (!target) return;
    try {
      await remove.mutateAsync({ challengeId, flagId: target.id });
      setFlags((list) => list.filter((f) => f.id !== target.id));
      setTarget(null);
      toast.success("Flag deleted");
    } catch (e) {
      toast.error("Could not delete the flag", messageOf(e));
    }
  };

  const columns: readonly Column<AdminFlag>[] = [
    { key: "type", header: "Type", cell: (f) => <Badge tone="info">{f.type}</Badge> },
    { key: "content", header: "Flag", cell: (f) => <code className="ff-mono">{f.content}</code> },
    {
      key: "ci",
      header: "Case",
      cell: (f) => (f.case_insensitive ? "insensitive" : "sensitive"),
    },
    {
      key: "actions",
      header: "Actions",
      headerHidden: true,
      align: "right",
      cell: (f) => (
        <div className="ff-row">
          <Button size="sm" onClick={() => setEditing(f)}>
            Edit
          </Button>
          <Button size="sm" variant="danger" onClick={() => setTarget(f)}>
            Delete
          </Button>
        </div>
      ),
    },
  ];

  return (
    <div className="ff-stack">
      <Alert tone="info" title="Flags are write-only">
        The API publishes no way to read a challenge's flags back — a stored flag is only ever
        compared, never returned. This table lists the flags added or changed in this session; the
        ones already on the challenge are still there, and still judging.
      </Alert>

      <Card title="Add a flag">
        <Form
          errors={errors}
          error={failure}
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
          footer={
            <Button type="submit" variant="primary" loading={add.isPending}>
              Add flag
            </Button>
          }
        >
          <Field name="type" label="Type">
            <Select
              value={type}
              onChange={(e) => setType(e.target.value as FlagType)}
              options={[
                { value: "static", label: "static — an exact string" },
                { value: "regex", label: "regex — a pattern" },
              ]}
            />
          </Field>

          <Field
            name="content"
            label={type === "regex" ? "Pattern" : "Flag"}
            required
            hint={
              type === "regex"
                ? "Checked here for obvious breakage only; the server's engine is the authority."
                : undefined
            }
          >
            <Input mono value={content} onChange={(e) => setContent(e.target.value)} />
          </Field>

          <Field name="case_insensitive" label="Case">
            <Checkbox
              checked={insensitive}
              onChange={(e) => setInsensitive(e.target.checked)}
              label="Match regardless of case"
            />
          </Field>
        </Form>
      </Card>

      <Card flush>
        <DataTable
          caption="Flags added in this session"
          columns={columns}
          rows={flags}
          rowKey={(f) => f.id}
          empty={
            <EmptyState
              title="No flags written yet"
              description="Add a flag above. A challenge with no flag can never be solved."
            />
          }
        />
      </Card>

      {editing && (
        <EditFlagDialog
          flag={editing}
          busy={update.isPending}
          onClose={() => setEditing(null)}
          onSave={async (body) => {
            try {
              const flag = await update.mutateAsync({ challengeId, flagId: editing.id, body });
              setFlags((list) => list.map((f) => (f.id === flag.id ? flag : f)));
              setEditing(null);
              toast.success("Flag saved");
              return null;
            } catch (e) {
              return isApiError(e) ? e.detail : messageOf(e);
            }
          }}
        />
      )}

      <ConfirmDestructive
        open={target !== null}
        onClose={() => setTarget(null)}
        onConfirm={() => void confirmDelete()}
        resourceKind="flag"
        resourceName={target?.content ?? ""}
        busy={remove.isPending}
      />
    </div>
  );
}

interface FlagEdit {
  type: FlagType;
  content: string;
  case_insensitive: boolean;
}

function EditFlagDialog({
  flag,
  busy,
  onClose,
  onSave,
}: {
  flag: AdminFlag;
  busy: boolean;
  onClose: () => void;
  onSave: (body: FlagEdit) => Promise<string | null>;
}) {
  const [type, setType] = useState<FlagType>(flag.type);
  const [content, setContent] = useState(flag.content);
  const [insensitive, setInsensitive] = useState(flag.case_insensitive);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [failure, setFailure] = useState<string | null>(null);

  const submit = async () => {
    const bad = flagProblem(type, content);
    setErrors(bad === null ? {} : { content: bad });
    if (bad !== null) return;
    setFailure(await onSave({ type, content, case_insensitive: insensitive }));
  };

  return (
    <Dialog open onClose={onClose} title="Edit flag">
      <Form
        errors={errors}
        error={failure}
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
        footer={
          <>
            <Button variant="ghost" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" loading={busy}>
              Save flag
            </Button>
          </>
        }
      >
        <Field name="type" label="Type">
          <Select
            value={type}
            onChange={(e) => setType(e.target.value as FlagType)}
            options={[
              { value: "static", label: "static" },
              { value: "regex", label: "regex" },
            ]}
          />
        </Field>
        <Field name="content" label={type === "regex" ? "Pattern" : "Flag"} required>
          <Input mono value={content} onChange={(e) => setContent(e.target.value)} />
        </Field>
        <Field name="case_insensitive" label="Case">
          <Checkbox
            checked={insensitive}
            onChange={(e) => setInsensitive(e.target.checked)}
            label="Match regardless of case"
          />
        </Field>
      </Form>
    </Dialog>
  );
}

// The server compiles the pattern with RE2 and its 422 is the verdict; this only stops a save that
// is obviously going nowhere.
function flagProblem(type: FlagType, content: string): string | null {
  if (content === "") return "A flag needs content.";
  if (type !== "regex") return null;
  try {
    new RegExp(content);
    return null;
  } catch (e) {
    return `Not a valid pattern: ${e instanceof Error ? e.message : String(e)}`;
  }
}

/* ------------------------------------------------------------------ hints */

type ReadHint = ChallengeDetail["hints"][number];

function HintsTab({ challengeId, read }: { challengeId: number; read: ChallengeDetail }) {
  const toast = useToast();
  const add = useAddHint();
  const update = useUpdateHint();
  const remove = useDeleteHint();

  const [title, setTitle] = useState("");
  const [content, setContent] = useState("");
  const [cost, setCost] = useState("0");
  const [errors, setErrors] = useState<FieldErrors>({});
  const [failure, setFailure] = useState<string | null>(null);

  const [editing, setEditing] = useState<ReadHint | null>(null);
  const [target, setTarget] = useState<ReadHint | null>(null);

  const hints = read.hints;
  const reordering = update.isPending;

  const submit = async () => {
    setFailure(null);
    const local: FieldErrors = {};
    if (content.trim() === "") local.content = "A hint needs a body.";
    if (!isCount(cost)) local.cost = "A whole number of points, zero or more.";
    setErrors(local);
    if (Object.keys(local).length > 0) return;

    try {
      await add.mutateAsync({
        challengeId,
        body: {
          content,
          cost: Number(cost),
          position: hints.length,
          ...(title === "" ? {} : { title }),
        },
      });
      setTitle("");
      setContent("");
      setCost("0");
      toast.success("Hint added");
    } catch (e) {
      if (isApiError(e)) {
        setErrors(fieldErrors({ errors: e.fieldErrors }));
        setFailure(e.detail);
        return;
      }
      setFailure(messageOf(e));
    }
  };

  // Position is the prerequisite chain: hint n is buyable only once n-1 is. Nothing reads the
  // stored positions back, so every hint is renumbered against its new index rather than trusting
  // two swapped rows to stay contiguous with the rest.
  const move = async (from: number, to: number) => {
    if (to < 0 || to >= hints.length) return;
    const next = [...hints];
    const [row] = next.splice(from, 1);
    next.splice(to, 0, row!);
    try {
      for (const [i, hint] of next.entries()) {
        await update.mutateAsync({ challengeId, hintId: hint.id, body: { position: i } });
      }
      toast.success("Hints reordered");
    } catch (e) {
      toast.error("Could not reorder the hints", messageOf(e));
    }
  };

  const confirmDelete = async () => {
    if (!target) return;
    try {
      await remove.mutateAsync({ challengeId, hintId: target.id });
      setTarget(null);
      toast.success("Hint deleted");
    } catch (e) {
      toast.error("Could not delete the hint", messageOf(e));
    }
  };

  const columns: readonly Column<ReadHint>[] = [
    {
      key: "order",
      header: "Order",
      width: "7rem",
      cell: (_h, i) => (
        <div className="ff-row">
          <Button
            size="sm"
            variant="ghost"
            aria-label={`Move hint ${i + 1} up`}
            disabled={i === 0 || reordering}
            onClick={() => void move(i, i - 1)}
          >
            ↑
          </Button>
          <Button
            size="sm"
            variant="ghost"
            aria-label={`Move hint ${i + 1} down`}
            disabled={i === hints.length - 1 || reordering}
            onClick={() => void move(i, i + 1)}
          >
            ↓
          </Button>
        </div>
      ),
    },
    {
      key: "title",
      header: "Title",
      cell: (h) => h.title ?? <span className="ff-muted">untitled</span>,
    },
    { key: "cost", header: "Cost", align: "right", cell: (h) => h.cost },
    {
      key: "actions",
      header: "Actions",
      headerHidden: true,
      align: "right",
      cell: (h) => (
        <div className="ff-row">
          <Button size="sm" onClick={() => setEditing(h)}>
            Edit
          </Button>
          <Button size="sm" variant="danger" onClick={() => setTarget(h)}>
            Delete
          </Button>
        </div>
      ),
    },
  ];

  return (
    <div className="ff-stack">
      <Card title="Add a hint">
        <Form
          errors={errors}
          error={failure}
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
          footer={
            <Button type="submit" variant="primary" loading={add.isPending}>
              Add hint
            </Button>
          }
        >
          <Field name="title" label="Title" hint="Optional. Players see it before they pay.">
            <Input value={title} onChange={(e) => setTitle(e.target.value)} />
          </Field>
          <Field name="content" label="Body" required>
            <Textarea value={content} onChange={(e) => setContent(e.target.value)} rows={4} />
          </Field>
          <Field name="cost" label="Cost" hint="Points charged on unlock. 0 is free.">
            <Input type="number" min={0} value={cost} onChange={(e) => setCost(e.target.value)} />
          </Field>
        </Form>
      </Card>

      <Card flush title="Hints, in unlock order">
        <DataTable
          caption="Hints, in the order they unlock"
          columns={columns}
          rows={hints}
          rowKey={(h) => h.id}
          empty={
            <EmptyState
              title="No hints"
              description="A hint costs points and unlocks in order: the one above it must be bought first."
            />
          }
        />
      </Card>

      {editing && (
        <EditHintDialog
          hint={editing}
          busy={update.isPending}
          onClose={() => setEditing(null)}
          onSave={async (body) => {
            try {
              await update.mutateAsync({ challengeId, hintId: editing.id, body });
              setEditing(null);
              toast.success("Hint saved");
              return null;
            } catch (e) {
              return isApiError(e) ? e.detail : messageOf(e);
            }
          }}
        />
      )}

      <ConfirmDestructive
        open={target !== null}
        onClose={() => setTarget(null)}
        onConfirm={() => void confirmDelete()}
        resourceKind="hint"
        resourceName={target?.title ?? `hint ${target?.id ?? ""}`}
        description="Players who already paid for it keep the charge."
        busy={remove.isPending}
      />
    </div>
  );
}

interface HintEdit {
  title?: string;
  content?: string;
  cost?: number;
}

function EditHintDialog({
  hint,
  busy,
  onClose,
  onSave,
}: {
  hint: ReadHint;
  busy: boolean;
  onClose: () => void;
  onSave: (body: HintEdit) => Promise<string | null>;
}) {
  const [title, setTitle] = useState(hint.title ?? "");
  const [content, setContent] = useState("");
  const [cost, setCost] = useState(String(hint.cost));
  const [errors, setErrors] = useState<FieldErrors>({});
  const [failure, setFailure] = useState<string | null>(null);

  const submit = async () => {
    if (!isCount(cost)) {
      setErrors({ cost: "A whole number of points, zero or more." });
      return;
    }
    setErrors({});
    setFailure(
      await onSave({
        title,
        cost: Number(cost),
        ...(content === "" ? {} : { content }),
      }),
    );
  };

  return (
    <Dialog open onClose={onClose} title="Edit hint">
      <Form
        errors={errors}
        error={failure}
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
        footer={
          <>
            <Button variant="ghost" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" loading={busy}>
              Save hint
            </Button>
          </>
        }
      >
        <Field name="title" label="Title">
          <Input value={title} onChange={(e) => setTitle(e.target.value)} />
        </Field>
        <Field
          name="content"
          label="Body"
          hint="A hint's body is never read back. Leave it blank to keep what is stored."
        >
          <Textarea value={content} onChange={(e) => setContent(e.target.value)} rows={4} />
        </Field>
        <Field name="cost" label="Cost">
          <Input type="number" min={0} value={cost} onChange={(e) => setCost(e.target.value)} />
        </Field>
      </Form>
    </Dialog>
  );
}

/* ------------------------------------------------------------------ files */

type ReadFile = ChallengeDetail["files"][number];

function FilesTab({ read }: { read: ChallengeDetail }) {
  const toast = useToast();
  const upload = useUploadFile();
  const remove = useDeleteFile();

  const [picked, setPicked] = useState<File | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [target, setTarget] = useState<ReadFile | null>(null);

  const send = async () => {
    if (!picked) return;
    setFailure(null);
    try {
      const file = await upload.mutateAsync({ challengeId: read.id, file: picked });
      setPicked(null);
      toast.success("File uploaded", file.name);
    } catch (e) {
      // 503 is not a hiccup: no object storage is wired on this deployment, and retrying will
      // never help. Say so instead of offering a retry.
      const message =
        isApiError(e) && e.status === 503 ? "file storage is not configured" : messageOf(e);
      setFailure(message);
      toast.error("Could not upload the file", message);
    }
  };

  const confirmDelete = async () => {
    if (!target) return;
    try {
      await remove.mutateAsync(target.id);
      setTarget(null);
      toast.success("File deleted", target.name);
    } catch (e) {
      const message =
        isApiError(e) && e.status === 503 ? "file storage is not configured" : messageOf(e);
      toast.error("Could not delete the file", message);
    }
  };

  const columns: readonly Column<ReadFile>[] = [
    { key: "name", header: "Name", cell: (f) => <span className="ff-mono">{f.name}</span> },
    { key: "size", header: "Size", align: "right", cell: (f) => bytes(f.size_bytes) },
    {
      key: "actions",
      header: "Actions",
      headerHidden: true,
      align: "right",
      cell: (f) => (
        <Button size="sm" variant="danger" onClick={() => setTarget(f)}>
          Delete
        </Button>
      ),
    },
  ];

  return (
    <div className="ff-stack">
      {failure !== null && (
        <Alert tone="danger" title="Upload failed" onDismiss={() => setFailure(null)}>
          {failure}
        </Alert>
      )}

      <Card title="Attach a file">
        <div className="ff-row">
          <input
            type="file"
            aria-label="File to attach"
            onChange={(e) => setPicked(e.target.files?.[0] ?? null)}
          />
          <Button
            variant="primary"
            disabled={picked === null}
            loading={upload.isPending}
            onClick={() => void send()}
          >
            Upload
          </Button>
        </div>
      </Card>

      <Card flush>
        <DataTable
          caption="Files attached to this challenge"
          columns={columns}
          rows={read.files}
          rowKey={(f) => f.id}
          empty={
            <EmptyState
              title="No files"
              description="Anything the challenge hands the player — a binary, a pcap, an image — goes here."
            />
          }
        />
      </Card>

      <ConfirmDestructive
        open={target !== null}
        onClose={() => setTarget(null)}
        onConfirm={() => void confirmDelete()}
        resourceKind="file"
        resourceName={target?.name ?? ""}
        busy={remove.isPending}
      />
    </div>
  );
}

/* ----------------------------------------------------------------- shared */

function isCount(value: string): boolean {
  return /^\d+$/.test(value.trim());
}

function optNum(n: number | null | undefined): string {
  return n === null || n === undefined ? "" : String(n);
}

function orNull(value: string): string | null {
  return value === "" ? null : value;
}

function numOrNull(value: string): number | null {
  return value === "" ? null : Number(value);
}

function bytes(n: number): string {
  const units = ["B", "KiB", "MiB", "GiB"];
  let size = n;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${unit === 0 ? size : size.toFixed(1)} ${units[unit]}`;
}

function messageOf(e: unknown): string {
  return isApiError(e) ? e.detail : e instanceof Error ? e.message : String(e);
}
