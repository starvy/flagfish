import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { isApiError } from "../../../api/client";
import {
  adminApi,
  type AdminChallenge,
  type AdminChallengeDetail,
  type AdminFlag,
  type AdminHint,
  type AdminInstance,
} from "../../../api/admin";
import {
  adminChallengeQuery,
  adminChallengesQuery,
  challengeInstancesQuery,
  useAddFlag,
  useAddHint,
  useAttachTag,
  useCreateChallenge,
  useDeleteAnnotation,
  useDeleteFile,
  useDeleteFlag,
  useDeleteHint,
  useDetachTag,
  useSetAnnotation,
  useSetChallengeFlagMode,
  useSetChallengeRequirements,
  useSetChallengeState,
  useUpdateChallenge,
  useUpdateFlag,
  useUpdateHint,
  useUploadFile,
  useUploadInstances,
} from "../../../queries";
import { PolicyGate, denialOf } from "../../../policy";
import { CountryPicker } from "../../../admin";
import { nameOf } from "../../../globe/geography";
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

  // The last body the server wrote back. It outranks the read: it is newer, and after a create it
  // carries the challenge before the detail query has caught up.
  const [saved, setSaved] = useState<AdminChallenge | null>(null);

  const detail = useQuery({ ...adminChallengeQuery(id ?? 0), enabled: id !== null, retry: false });

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
    return denialOf(detail.error) ? (
      <PolicyGate error={detail.error} />
    ) : (
      <Alert tone="danger" title="Could not load the challenge">
        <p>{messageOf(detail.error)}</p>
        <Button onClick={() => void detail.refetch()}>Retry</Button>
      </Alert>
    );
  }

  const data = detail.data ?? null;
  const read = data?.challenge ?? null;
  // The write-back is newer than the read; fall through to the read, then to nothing for a new one.
  const current = saved ?? read;
  const state = current?.state ?? "visible";
  const name = current?.name ?? "New challenge";

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
            content: <DetailsTab id={id} current={current} onSaved={setSaved} />,
          },
          {
            id: "requirements",
            label: "Requirements",
            disabled: id === null,
            content:
              id === null ? null : (
                <RequirementsTab challengeId={id} current={current} onSaved={setSaved} />
              ),
          },
          {
            id: "flags",
            label: "Flags",
            disabled: id === null,
            content: id === null ? null : <FlagsTab challengeId={id} flags={data?.flags ?? []} />,
          },
          {
            id: "pool",
            label: "Pool",
            disabled: id === null,
            content:
              id === null ? null : (
                <PoolTab challengeId={id} current={current} onSaved={setSaved} />
              ),
          },
          {
            id: "tags",
            label: "Tags",
            disabled: id === null,
            content: id === null ? null : <TagsTab challengeId={id} tags={data?.tags ?? []} />,
          },
          {
            id: "country",
            label: "Country",
            disabled: id === null,
            content:
              id === null ? null : (
                <CountryTab challengeId={id} annotations={data?.annotations ?? {}} />
              ),
          },
          {
            id: "hints",
            label: "Hints",
            disabled: id === null,
            content: id === null ? null : <HintsTab challengeId={id} hints={data?.hints ?? []} />,
          },
          {
            id: "files",
            label: "Files",
            disabled: id === null,
            content: id === null ? null : <FilesTab challengeId={id} files={data?.files ?? []} />,
          },
        ]}
      />
    </div>
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
  logic: "any" | "all";
  first_blood: "none" | "announce" | "bonus";
  first_blood_bonus: string;
  // The suggested-next challenge id, or "" for none. PATCH-only, so it is only offered while
  // editing an existing challenge.
  next_id: string;
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
  first_blood: "none",
  first_blood_bonus: "",
  next_id: "",
  value: "0",
  initial: "",
  minimum: "",
  decay: "",
  max_attempts: "0",
  position: "",
};

// The admin read carries the whole row — logic, position, the decay parameters — so every field
// seeds from it directly. A field the operator never touches is omitted from the PATCH, which is
// exactly the server's "omit keeps" rule.
function seedOf(ch: AdminChallenge | null): FormState {
  if (!ch) return BLANK;
  return {
    name: ch.name,
    category: ch.category,
    description: ch.description ?? "",
    attribution: ch.attribution ?? "",
    connection_info: ch.connection_info ?? "",
    state: ch.state as FormState["state"],
    function: ch.function as FormState["function"],
    logic: ch.logic as FormState["logic"],
    first_blood: ch.first_blood as FormState["first_blood"],
    first_blood_bonus: optNum(ch.first_blood_bonus),
    next_id: ch.next_id != null ? String(ch.next_id) : "",
    value: String(ch.value),
    initial: optNum(ch.initial),
    minimum: optNum(ch.minimum),
    decay: optNum(ch.decay),
    max_attempts: String(ch.max_attempts),
    position: String(ch.position),
  };
}

function DetailsTab({
  id,
  current,
  onSaved,
}: {
  id: number | null;
  current: AdminChallenge | null;
  onSaved: (ch: AdminChallenge) => void;
}) {
  const navigate = useNavigate();
  const toast = useToast();
  const create = useCreateChallenge();
  const update = useUpdateChallenge();
  const setState = useSetChallengeState();
  // Other challenges to suggest as the next one. Self is filtered out client-side; the CHECK is the
  // real arbiter. Only needed once the challenge exists, since next_id is PATCH-only.
  const board = useQuery({ ...adminChallengesQuery, enabled: id !== null });

  const [base, setBase] = useState<FormState>(() => seedOf(current));
  const [form, setForm] = useState<FormState>(base);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [failure, setFailure] = useState<string | null>(null);

  const set = <K extends keyof FormState>(key: K, value: FormState[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  const accept = (ch: AdminChallenge) => {
    const next = seedOf(ch);
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
          logic: form.logic,
          first_blood: form.first_blood,
          ...(form.first_blood === "bonus"
            ? { first_blood_bonus: Number(form.first_blood_bonus) }
            : {}),
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

      let ch = current;
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

        <Field
          name="first_blood"
          label="First blood"
          hint="Enabling this on an already-solved challenge changes nothing retroactively: the first solve is a stamped fact, so nobody is paid or announced after the fact."
        >
          <Select
            value={form.first_blood}
            onChange={(e) => set("first_blood", e.target.value as FormState["first_blood"])}
            options={[
              { value: "none", label: "none — first solve is just a solve" },
              { value: "announce", label: "announce — the first solver is broadcast" },
              { value: "bonus", label: "bonus — announced and paid extra points" },
            ]}
          />
        </Field>

        {form.first_blood === "bonus" && (
          <Field name="first_blood_bonus" label="First blood bonus" required hint="Extra points for the first solver. Must be positive.">
            <Input
              type="number"
              min={1}
              value={form.first_blood_bonus}
              onChange={(e) => set("first_blood_bonus", e.target.value)}
            />
          </Field>
        )}

        {id !== null && (
          <Field
            name="next_id"
            label="Next challenge"
            hint="Suggested to a player right after they solve this one. Optional."
          >
            <Select
              value={form.next_id}
              onChange={(e) => set("next_id", e.target.value)}
              options={[
                { value: "", label: "none" },
                ...(board.data?.challenges ?? [])
                  .filter((c) => c.id !== id)
                  .map((c) => ({ value: String(c.id), label: `${c.name} (${c.category})` })),
              ]}
            />
          </Field>
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
  if (form.first_blood === "bonus" && (!isCount(form.first_blood_bonus) || Number(form.first_blood_bonus) < 1)) {
    out.first_blood_bonus = "A bonus is a positive number of points.";
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
  if (form.logic !== base.logic) patch.logic = form.logic;
  if (form.first_blood !== base.first_blood) {
    patch.first_blood = form.first_blood;
    // The bonus must switch in the same PATCH: bonus mode carries its value, any other mode
    // clears it with an explicit null — a half-switched row is refused by the server.
    patch.first_blood_bonus = form.first_blood === "bonus" ? Number(form.first_blood_bonus) : null;
  } else if (form.first_blood === "bonus" && form.first_blood_bonus !== base.first_blood_bonus) {
    patch.first_blood_bonus = Number(form.first_blood_bonus);
  }
  if (form.next_id !== base.next_id) patch.next_id = form.next_id === "" ? null : Number(form.next_id);
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

/* ----------------------------------------------------------- requirements */

type ReqVisibility = "hidden" | "masked" | "preview";

function RequirementsTab({
  challengeId,
  current,
  onSaved,
}: {
  challengeId: number;
  current: AdminChallenge | null;
  onSaved: (ch: AdminChallenge) => void;
}) {
  const board = useQuery(adminChallengesQuery);
  const save = useSetChallengeRequirements();
  const toast = useToast();

  const stored = current?.requirements ?? null;
  const [selected, setSelected] = useState<number[]>(stored?.prerequisites ?? []);
  const [visibility, setVisibility] = useState<ReqVisibility>(
    (stored?.visibility as ReqVisibility | undefined) ?? "hidden",
  );
  const [warnings, setWarnings] = useState<string[]>([]);
  const [failure, setFailure] = useState<string | null>(null);

  const options = (board.data?.challenges ?? []).filter((c) => c.id !== challengeId);

  const toggle = (id: number) =>
    setSelected((list) => (list.includes(id) ? list.filter((x) => x !== id) : [...list, id]));

  const submit = async () => {
    setFailure(null);
    try {
      const out = await save.mutateAsync({
        id: challengeId,
        body: { prerequisites: selected, visibility },
      });
      onSaved(out.challenge);
      setSelected(out.challenge.requirements.prerequisites ?? []);
      setWarnings(out.warnings ?? []);
      toast.success("Requirements saved");
    } catch (e) {
      setFailure(messageOf(e));
    }
  };

  return (
    <div className="ff-stack">
      {warnings.map((w) => (
        <Alert key={w} tone="warn" title="Saved, with a warning">
          {w}
        </Alert>
      ))}

      <Card title="Prerequisites">
        <Form
          errors={{}}
          error={failure}
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
          footer={
            <Button type="submit" variant="primary" loading={save.isPending}>
              Save requirements
            </Button>
          }
        >
          <Field
            name="prerequisites"
            label="Must be solved first"
            hint="Every selected challenge has to be solved before this one opens."
          >
            {board.isPending ? (
              <Skeleton lines={3} height="1.25rem" />
            ) : options.length === 0 ? (
              <p className="ff-muted">There is no other challenge to require.</p>
            ) : (
              <div className="ff-stack">
                {options.map((c) => (
                  <Checkbox
                    key={c.id}
                    checked={selected.includes(c.id)}
                    onChange={() => toggle(c.id)}
                    label={`${c.name} (${c.category})`}
                  />
                ))}
              </div>
            )}
          </Field>

          <Field
            name="visibility"
            label="While locked"
            hint="hidden: players never learn it exists. masked: listed as ??? with nothing solvable. preview: real name shown so a route can be planned."
          >
            <Select
              value={visibility}
              onChange={(e) => setVisibility(e.target.value as ReqVisibility)}
              options={[
                { value: "hidden", label: "hidden — off the board entirely" },
                { value: "masked", label: "masked — on the board as ???" },
                { value: "preview", label: "preview — named, but locked" },
              ]}
            />
          </Field>
        </Form>
      </Card>
    </div>
  );
}

/* ------------------------------------------------------------------- tags */

function TagsTab({ challengeId, tags: initial }: { challengeId: number; tags: string[] }) {
  const toast = useToast();
  const attach = useAttachTag();
  const detach = useDetachTag();

  // Seeded from the read, then kept in step locally so attach/detach show immediately.
  const [tags, setTags] = useState<string[]>(initial);
  const [value, setValue] = useState("");
  const [failure, setFailure] = useState<string | null>(null);

  const add = async () => {
    const v = value.trim();
    if (v === "") return;
    setFailure(null);
    try {
      const tag = await attach.mutateAsync({ challengeId, value: v });
      setTags((list) => (list.includes(tag.value) ? list : [...list, tag.value]));
      setValue("");
      toast.success("Tag attached", tag.value);
    } catch (e) {
      setFailure(messageOf(e));
    }
  };

  const remove = async (v: string) => {
    try {
      await detach.mutateAsync({ challengeId, value: v });
      setTags((list) => list.filter((t) => t !== v));
      toast.success("Tag detached", v);
    } catch (e) {
      toast.error("Could not detach the tag", messageOf(e));
    }
  };

  return (
    <div className="ff-stack">
      {failure !== null && (
        <Alert tone="danger" title="Could not attach the tag" onDismiss={() => setFailure(null)}>
          {failure}
        </Alert>
      )}

      <Card title="Attach a tag">
        <form
          className="ff-row"
          onSubmit={(e) => {
            e.preventDefault();
            void add();
          }}
        >
          <Input
            value={value}
            onChange={(e) => setValue(e.target.value)}
            aria-label="Tag value"
            placeholder="e.g. web"
          />
          <Button
            type="submit"
            variant="primary"
            loading={attach.isPending}
            disabled={value.trim() === ""}
          >
            Attach
          </Button>
        </form>
      </Card>

      <Card title="Tags on this challenge">
        {tags.length === 0 ? (
          <p className="ff-muted">No tags yet. Players see them on the challenge detail.</p>
        ) : (
          <div className="ff-row">
            {tags.map((t) => (
              <span key={t} className="ff-row">
                <Badge tone="info">{t}</Badge>
                <Button
                  size="sm"
                  variant="ghost"
                  aria-label={`Detach tag ${t}`}
                  onClick={() => void remove(t)}
                >
                  ×
                </Button>
              </span>
            ))}
          </div>
        )}
      </Card>
    </div>
  );
}

/* ---------------------------------------------------------------- country */

/** The well-known annotation key a view places a challenge by. */
const COUNTRY_KEY = "country";

function CountryTab({
  challengeId,
  annotations,
}: {
  challengeId: number;
  annotations: Record<string, string>;
}) {
  const toast = useToast();
  const setAnnotation = useSetAnnotation();
  const deleteAnnotation = useDeleteAnnotation();

  // Seeded from the read, then kept in step locally so a save shows immediately.
  const [country, setCountry] = useState<string | null>(annotations[COUNTRY_KEY] ?? null);
  const [failure, setFailure] = useState<string | null>(null);
  const busy = setAnnotation.isPending || deleteAnnotation.isPending;

  const apply = async (next: string | null) => {
    setFailure(null);
    try {
      // Clearing the box is a delete, not an empty value: an annotation nobody set and an
      // annotation set to nothing must not be two different states on the wire.
      if (next === null) {
        await deleteAnnotation.mutateAsync({ challengeId, key: COUNTRY_KEY });
        toast.success("Country cleared", "This challenge is no longer placed.");
      } else {
        await setAnnotation.mutateAsync({ challengeId, key: COUNTRY_KEY, value: next });
        toast.success("Country saved", `${nameOf(next)} (${next})`);
      }
      setCountry(next);
    } catch (e) {
      setFailure(messageOf(e));
    }
  };

  return (
    <div className="ff-stack">
      {failure !== null && (
        <Alert tone="danger" title="Could not save the country" onDismiss={() => setFailure(null)}>
          {failure}
        </Alert>
      )}

      <Card
        title="Where this challenge is"
        footer={
          <span className="ff-muted">
            Only the globe view of the board uses this. A challenge with no country is still on
            the board — the globe lists it beside the map rather than hiding it.
          </span>
        }
      >
        <Field name="country" label="Country">
          {() => (
            <CountryPicker value={country} onChange={(next) => void apply(next)} disabled={busy} />
          )}
        </Field>

        {country !== null && (
          <p>
            Placed in <Badge tone="info">{nameOf(country)}</Badge>
          </p>
        )}
      </Card>
    </div>
  );
}

/* ------------------------------------------------------------------ flags */

function FlagsTab({ challengeId, flags }: { challengeId: number; flags: AdminFlag[] }) {
  const toast = useToast();
  const add = useAddFlag();
  const update = useUpdateFlag();
  const remove = useDeleteFlag();

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
      await add.mutateAsync({
        challengeId,
        body: { type, content, case_insensitive: insensitive },
      });
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
          caption="Flags on this challenge"
          columns={columns}
          rows={flags}
          rowKey={(f) => f.id}
          empty={
            <EmptyState
              title="No flags yet"
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
              await update.mutateAsync({ challengeId, flagId: editing.id, body });
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
  const [type, setType] = useState<FlagType>(flag.type as FlagType);
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

function HintsTab({ challengeId, hints }: { challengeId: number; hints: AdminHint[] }) {
  const toast = useToast();
  const add = useAddHint();
  const update = useUpdateHint();
  const remove = useDeleteHint();

  const [title, setTitle] = useState("");
  const [content, setContent] = useState("");
  const [cost, setCost] = useState("0");
  const [prereqIds, setPrereqIds] = useState<number[]>([]);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [failure, setFailure] = useState<string | null>(null);

  const [editing, setEditing] = useState<AdminHint | null>(null);
  const [target, setTarget] = useState<AdminHint | null>(null);

  const reordering = update.isPending;

  const togglePrereq = (id: number) =>
    setPrereqIds((list) => (list.includes(id) ? list.filter((x) => x !== id) : [...list, id]));

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
          ...(prereqIds.length === 0 ? {} : { prerequisites: prereqIds }),
        },
      });
      setTitle("");
      setContent("");
      setCost("0");
      setPrereqIds([]);
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

  // Position is the prerequisite chain: hint n is buyable only once n-1 is. Every hint is renumbered
  // against its new index rather than trusting two swapped rows to stay contiguous with the rest.
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

  const columns: readonly Column<AdminHint>[] = [
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
          {hints.length > 0 && (
            <Field
              name="prerequisites"
              label="Unlock after"
              hint="Every selected hint must be unlocked before this one can be bought."
            >
              <div className="ff-stack">
                {hints.map((h) => (
                  <Checkbox
                    key={h.id}
                    checked={prereqIds.includes(h.id)}
                    onChange={() => togglePrereq(h.id)}
                    label={h.title ?? `hint ${h.id}`}
                  />
                ))}
              </div>
            </Field>
          )}
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
          others={hints.filter((h) => h.id !== editing.id)}
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
  prerequisites?: number[];
}

function EditHintDialog({
  hint,
  others,
  busy,
  onClose,
  onSave,
}: {
  hint: AdminHint;
  others: AdminHint[];
  busy: boolean;
  onClose: () => void;
  onSave: (body: HintEdit) => Promise<string | null>;
}) {
  const [title, setTitle] = useState(hint.title ?? "");
  const [content, setContent] = useState(hint.content);
  const [cost, setCost] = useState(String(hint.cost));
  // The stored set is three-state like the body: untouched is omitted (keeps), and any touch
  // replaces the whole set with the selection — empty clears.
  const [prereqTouched, setPrereqTouched] = useState(false);
  const [prereqIds, setPrereqIds] = useState<number[]>(hint.prerequisites ?? []);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [failure, setFailure] = useState<string | null>(null);

  const togglePrereq = (id: number) => {
    setPrereqTouched(true);
    setPrereqIds((list) => (list.includes(id) ? list.filter((x) => x !== id) : [...list, id]));
  };

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
        ...(prereqTouched ? { prerequisites: prereqIds } : {}),
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
        <Field name="content" label="Body">
          <Textarea value={content} onChange={(e) => setContent(e.target.value)} rows={4} />
        </Field>
        <Field name="cost" label="Cost">
          <Input type="number" min={0} value={cost} onChange={(e) => setCost(e.target.value)} />
        </Field>
        {others.length > 0 && (
          <Field
            name="prerequisites"
            label="Unlock after"
            hint="Every selected hint must be unlocked before this one can be bought."
          >
            <div className="ff-stack">
              {others.map((h) => (
                <Checkbox
                  key={h.id}
                  checked={prereqIds.includes(h.id)}
                  onChange={() => togglePrereq(h.id)}
                  label={h.title ?? `hint ${h.id}`}
                />
              ))}
            </div>
          </Field>
        )}
      </Form>
    </Dialog>
  );
}

/* ------------------------------------------------------------------ files */

type ReadFile = NonNullable<AdminChallengeDetail["files"]>[number];

function FilesTab({ challengeId, files }: { challengeId: number; files: ReadFile[] }) {
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
      const file = await upload.mutateAsync({ challengeId, file: picked });
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
          rows={files}
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

/* ------------------------------------------------------------------- pool */

function currentFlagMode(current: AdminChallenge | null): "static" | "unique" {
  return current?.flag_mode === "unique" ? "unique" : "static";
}

function PoolTab({
  challengeId,
  current,
  onSaved,
}: {
  challengeId: number;
  current: AdminChallenge | null;
  onSaved: (ch: AdminChallenge) => void;
}) {
  const toast = useToast();
  const setMode = useSetChallengeFlagMode();
  const upload = useUploadInstances();
  const instances = useQuery(challengeInstancesQuery(challengeId, { per_page: 100 }));

  const mode = currentFlagMode(current);
  const [flagsText, setFlagsText] = useState("");
  const [result, setResult] = useState<{
    generation: number;
    inserted: number;
    idempotent: boolean;
    warnings: string[];
  } | null>(null);
  const [failure, setFailure] = useState<string | null>(null);

  const switchMode = async (next: "static" | "unique") => {
    try {
      const ch = await setMode.mutateAsync({ id: challengeId, flagMode: next });
      onSaved(ch);
      toast.success("Flag mode changed", `now ${next}`);
    } catch (e) {
      // The guard failures (regex flag present, no flags, has solves, logic=all) all arrive as a
      // 422 with a plain reason — show it verbatim, it is written for the operator.
      toast.error("Could not change the flag mode", messageOf(e));
    }
  };

  const submitPool = async () => {
    setFailure(null);
    setResult(null);
    const flags = flagsText
      .split("\n")
      .map((line) => line.trim())
      .filter((line) => line !== "");
    if (flags.length === 0) {
      setFailure("Paste at least one flag, one per line.");
      return;
    }
    if (new Set(flags).size !== flags.length) {
      setFailure("The list has a duplicate flag. Every instance must be distinct.");
      return;
    }
    try {
      // Hash in the browser: the plaintext flag never leaves this tab, only its sha256 does.
      const hashes = await Promise.all(flags.map(sha256Hex));
      const out = await upload.mutateAsync({
        challengeId,
        body: { instances: hashes.map((value_hash) => ({ value_hash })) },
      });
      setResult({
        generation: out.generation,
        inserted: out.inserted,
        idempotent: out.idempotent,
        warnings: out.warnings ?? [],
      });
      setFlagsText("");
      toast.success(
        out.idempotent ? "Pool unchanged" : "Pool uploaded",
        out.idempotent
          ? `already at generation ${out.generation}`
          : `${out.inserted} instances in generation ${out.generation}`,
      );
    } catch (e) {
      setFailure(messageOf(e));
    }
  };

  const rows = instances.data?.instances ?? [];
  const columns: readonly Column<AdminInstance>[] = [
    { key: "gen", header: "Gen", align: "right", cell: (i) => i.generation },
    {
      key: "hash",
      header: "Flag hash",
      cell: (i) => <code className="ff-mono">{i.value_hash.slice(0, 16)}…</code>,
    },
    {
      key: "issued",
      header: "Issued to",
      cell: (i) =>
        i.issued_to === undefined ? (
          <span className="ff-muted">free</span>
        ) : (
          <Badge tone="info">account {i.issued_to}</Badge>
        ),
    },
  ];

  return (
    <div className="ff-stack">
      <Card title="Flag mode">
        <div className="ff-stack">
          <p>
            This challenge issues{" "}
            {mode === "unique" ? (
              <Badge tone="info">unique — one flag per account</Badge>
            ) : (
              <Badge tone="neutral">static — one shared flag</Badge>
            )}
            .
          </p>
          <p className="ff-muted">
            A switch is refused while the challenge has any solve, and — toward unique — while a regex
            flag exists or the flag logic is “all”. Toward static, the challenge must still have a
            flag. Deleting and recreating the challenge is the escape hatch once it has solves.
          </p>
          <div className="ff-row">
            <Button
              variant={mode === "static" ? "primary" : "secondary"}
              disabled={mode === "static" || setMode.isPending}
              onClick={() => void switchMode("static")}
            >
              Use static flags
            </Button>
            <Button
              variant={mode === "unique" ? "primary" : "secondary"}
              disabled={mode === "unique" || setMode.isPending}
              onClick={() => void switchMode("unique")}
            >
              Use unique flags
            </Button>
          </div>
        </div>
      </Card>

      {mode === "unique" && rows.length === 0 && !instances.isPending && (
        <Alert tone="warn" title="This pool is empty">
          A unique-flag challenge with no instances hands out a hard 503 on the first view. Upload a
          pool below before the event opens.
        </Alert>
      )}

      <Card title="Upload instances">
        <Form
          errors={{}}
          error={failure}
          onSubmit={(e) => {
            e.preventDefault();
            void submitPool();
          }}
          footer={
            <Button type="submit" variant="primary" loading={upload.isPending}>
              Hash &amp; upload
            </Button>
          }
        >
          <Field
            name="flags"
            label="Flags, one per line"
            hint="Each line is hashed in your browser with SHA-256; only the hash is uploaded, never the plaintext. Re-uploading the same set is a no-op. A new set becomes the next generation, and new players draw from it."
          >
            <Textarea
              mono
              value={flagsText}
              onChange={(e) => setFlagsText(e.target.value)}
              rows={8}
              placeholder={"flag{one}\nflag{two}\nflag{three}"}
            />
          </Field>
        </Form>

        {result !== null && (
          <Alert
            tone={result.idempotent ? "info" : "success"}
            title={result.idempotent ? "Nothing to upload" : "Pool uploaded"}
            onDismiss={() => setResult(null)}
          >
            <div className="pool-upload__result">
              <Badge tone="neutral">generation {result.generation}</Badge>
              <span>{result.inserted} instances added</span>
            </div>
            {result.warnings.map((w) => (
              <p key={w} className="ff-muted">
                {w}
              </p>
            ))}
          </Alert>
        )}
      </Card>

      <Card flush title="Instances">
        <DataTable
          caption="Instances in this challenge's pool"
          columns={columns}
          rows={rows}
          rowKey={(i) => i.id}
          empty={
            <EmptyState
              title="No instances"
              description="Upload a pool above. Each instance is one account's flag; the pool must be at least as large as the field."
            />
          }
        />
      </Card>
    </div>
  );
}

// sha256Hex hashes a string to lowercase hex with the Web Crypto API, matching the server's
// sha256(flag) probe. The flag is trimmed by the caller, mirroring the server's whitespace
// normalisation, so a stray newline never produces a hash that will not match.
async function sha256Hex(text: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  return Array.from(new Uint8Array(digest))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
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
