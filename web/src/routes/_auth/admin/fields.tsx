import { useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import type { AdminField } from "../../../api/admin";
import { isApiError } from "../../../api/errors";
import { denialOf, PolicyGate } from "../../../policy";
import {
  adminFieldsQuery,
  useCreateField,
  useDeleteField,
  useUpdateField,
} from "../../../queries";
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
  Select,
  useToast,
  type Column,
  type FieldErrors,
} from "../../../ui";

export const Route = createFileRoute("/_auth/admin/fields")({
  component: FieldsPage,
});

type AppliesTo = "user" | "team";
type FieldType = "text" | "boolean";

function FieldsPage() {
  const toast = useToast();

  const fields = useQuery(adminFieldsQuery);
  const rows = fields.data?.fields ?? [];

  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<AdminField | null>(null);
  const [deleting, setDeleting] = useState<AdminField | null>(null);

  const create = useCreateField();
  const update = useUpdateField();
  const remove = useDeleteField();

  const columns: readonly Column<AdminField>[] = [
    { key: "name", header: "name", cell: (f) => f.name },
    { key: "applies_to", header: "applies to", cell: (f) => <Badge tone="neutral">{f.applies_to}</Badge> },
    { key: "field_type", header: "type", cell: (f) => <Badge tone="neutral">{f.field_type}</Badge> },
    {
      key: "flags",
      header: "flags",
      cell: (f) => (
        <span className="ff-row">
          {f.required && <Badge tone="warn">required</Badge>}
          {f.public && <Badge tone="accent">public</Badge>}
          {f.editable && <Badge tone="neutral">editable</Badge>}
        </span>
      ),
    },
    {
      key: "actions",
      header: "Actions",
      headerHidden: true,
      align: "right",
      cell: (f) => (
        <span className="ff-row">
          <Button size="sm" variant="ghost" onClick={() => setEditing(f)}>
            Edit
          </Button>
          <Button size="sm" variant="danger" onClick={() => setDeleting(f)}>
            Delete
          </Button>
        </span>
      ),
    },
  ];

  if (fields.isError) {
    if (denialOf(fields.error) !== null) return <PolicyGate error={fields.error} />;
    return (
      <Alert tone="danger" title="Could not load fields">
        <p>{messageOf(fields.error)}</p>
        <Button size="sm" onClick={() => void fields.refetch()}>
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
        toast.success(`Deleted ${name}`, "Its answers were removed with it.");
      },
      onError: (error) => toast.error("Could not delete the field", messageOf(error)),
    });
  };

  return (
    <>
      <div className="page-head">
        <h1>registration fields</h1>
        <Button variant="primary" onClick={() => setCreating(true)}>
          New field
        </Button>
      </div>

      <Card flush>
        <DataTable
          caption="Custom registration fields"
          columns={columns}
          rows={rows}
          rowKey={(f) => f.id}
          loading={fields.isPending}
          empty={
            <EmptyState
              title="No custom fields yet"
              description="A field collects an extra answer at sign-up — affiliation, eligibility, a consent checkbox."
              action={
                <Button variant="primary" onClick={() => setCreating(true)}>
                  New field
                </Button>
              }
            />
          }
        />
      </Card>

      {creating && (
        <FieldDialog
          busy={create.isPending}
          onClose={() => setCreating(false)}
          onSubmit={(body, onError) =>
            create.mutate(
              { ...body, position: 0 },
              {
                onSuccess: (f) => {
                  setCreating(false);
                  toast.success(`Created ${f.name}`);
                },
                onError,
              },
            )
          }
        />
      )}

      {editing !== null && (
        <FieldDialog
          field={editing}
          busy={update.isPending}
          onClose={() => setEditing(null)}
          onSubmit={(body, onError) =>
            update.mutate(
              {
                id: editing.id,
                body: {
                  name: body.name,
                  description: body.description,
                  required: body.required,
                  public: body.public,
                  editable: body.editable,
                },
              },
              {
                onSuccess: (f) => {
                  setEditing(null);
                  toast.success(`Saved ${f.name}`);
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
          resourceKind="field"
          busy={remove.isPending}
          description="Every answer to this field is removed. No account, solve or score is touched."
        />
      )}
    </>
  );
}

interface FieldBody {
  name: string;
  applies_to: AppliesTo;
  field_type: FieldType;
  description?: string;
  required: boolean;
  public: boolean;
  editable: boolean;
}

interface FieldDialogProps {
  field?: AdminField;
  busy: boolean;
  onClose: () => void;
  onSubmit: (body: FieldBody, onError: (error: unknown) => void) => void;
}

function FieldDialog({ field, busy, onClose, onSubmit }: FieldDialogProps) {
  const editing = field !== undefined;
  const [name, setName] = useState(field?.name ?? "");
  const [description, setDescription] = useState(field?.description ?? "");
  const [appliesTo, setAppliesTo] = useState<AppliesTo>(asAppliesTo(field?.applies_to) ?? "user");
  const [fieldType, setFieldType] = useState<FieldType>(asFieldType(field?.field_type) ?? "text");
  const [required, setRequired] = useState(field?.required ?? false);
  const [isPublic, setIsPublic] = useState(field?.public ?? false);
  const [editable, setEditable] = useState(field?.editable ?? false);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [error, setError] = useState<string | null>(null);

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setErrors({});
    setError(null);
    onSubmit(
      {
        name: name.trim(),
        applies_to: appliesTo,
        field_type: fieldType,
        description: description.trim() === "" ? undefined : description.trim(),
        required,
        public: isPublic,
        editable,
      },
      (failure) => {
        setErrors(fieldErrorsOf(failure));
        setError(messageOf(failure));
      },
    );
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title={editing ? "Edit field" : "New field"}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" form="field-form" variant="primary" loading={busy}>
            {editing ? "Save" : "Create"}
          </Button>
        </>
      }
    >
      <Form id="field-form" onSubmit={submit} errors={errors} error={error}>
        <Field name="name" label="Name" required>
          <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={128} />
        </Field>

        <Field name="description" label="Description" hint="Help text shown under the input.">
          <Input value={description} onChange={(e) => setDescription(e.target.value)} maxLength={500} />
        </Field>

        <Field
          name="applies_to"
          label="Applies to"
          required
          hint={editing ? "Fixed at creation — an answer's owner cannot change." : "Who answers this field."}
        >
          <Select
            value={appliesTo}
            disabled={editing}
            options={[
              { value: "user", label: "user" },
              { value: "team", label: "team" },
            ]}
            onChange={(e) => setAppliesTo(e.target.value as AppliesTo)}
          />
        </Field>

        <Field
          name="field_type"
          label="Type"
          required
          hint={editing ? "Fixed at creation — changing it would misread existing answers." : undefined}
        >
          <Select
            value={fieldType}
            disabled={editing}
            options={[
              { value: "text", label: "text" },
              { value: "boolean", label: "checkbox" },
            ]}
            onChange={(e) => setFieldType(e.target.value as FieldType)}
          />
        </Field>

        <Field
          name="required"
          label="Required"
          hint="A required field must be answered to reach the challenges."
        >
          <Checkbox
            checked={required}
            label="Answer is mandatory"
            onChange={(e) => setRequired(e.target.checked)}
          />
        </Field>

        <Field name="public" label="Public" hint="Shown on the player's public profile.">
          <Checkbox
            checked={isPublic}
            label="Answer is public"
            onChange={(e) => setIsPublic(e.target.checked)}
          />
        </Field>

        <Field name="editable" label="Editable" hint="The player can change the answer after sign-up.">
          <Checkbox
            checked={editable}
            label="Answer is editable"
            onChange={(e) => setEditable(e.target.checked)}
          />
        </Field>
      </Form>
    </Dialog>
  );
}

function asAppliesTo(value: unknown): AppliesTo | null {
  return value === "user" || value === "team" ? value : null;
}

function asFieldType(value: unknown): FieldType | null {
  return value === "text" || value === "boolean" ? value : null;
}

function fieldErrorsOf(error: unknown): FieldErrors {
  return isApiError(error) ? fieldErrors({ errors: error.fieldErrors }) : {};
}

function messageOf(error: unknown): string {
  if (isApiError(error)) return error.detail;
  return error instanceof Error ? error.message : "unexpected error";
}
