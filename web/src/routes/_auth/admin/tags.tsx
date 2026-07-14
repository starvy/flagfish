import { useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import type { AdminTag } from "../../../api/admin";
import { isApiError } from "../../../api/errors";
import { denialOf, PolicyGate } from "../../../policy";
import { adminTagsQuery, useDeleteTag, useMergeTag } from "../../../queries";
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
  useToast,
  type Column,
  type FieldErrors,
} from "../../../ui";

export const Route = createFileRoute("/_auth/admin/tags")({
  component: TagsPage,
});

function TagsPage() {
  const toast = useToast();
  const tags = useQuery(adminTagsQuery);
  const rows = tags.data?.tags ?? [];

  const [merging, setMerging] = useState<AdminTag | null>(null);
  const [deleting, setDeleting] = useState<AdminTag | null>(null);
  // Force is never offered up front: the operator earns it by hitting the 409 that explains it.
  const [force, setForce] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);

  const remove = useDeleteTag();

  const openDelete = (tag: AdminTag) => {
    setForce(false);
    setDeleteError(null);
    setDeleting(tag);
  };

  const confirmDelete = () => {
    if (deleting === null) return;
    const value = deleting.value;
    remove.mutate(
      { value, force },
      {
        onSuccess: () => {
          setDeleting(null);
          toast.success(`Deleted ${value}`);
        },
        onError: (error) => {
          if (isApiError(error) && error.status === 409) {
            setForce(true);
            setDeleteError(error.detail);
            return;
          }
          toast.error("Could not delete the tag", messageOf(error));
        },
      },
    );
  };

  const columns: readonly Column<AdminTag>[] = [
    {
      key: "value",
      header: "tag",
      cell: (t) => <span className="ff-mono">{t.value}</span>,
    },
    {
      key: "uses",
      header: "challenges",
      align: "right",
      width: "10rem",
      cell: (t) => <Badge tone={t.uses === 0 ? "neutral" : "accent"}>{t.uses}</Badge>,
    },
    {
      key: "actions",
      header: "Actions",
      headerHidden: true,
      align: "right",
      cell: (t) => (
        <span className="ff-row">
          <Button size="sm" variant="ghost" onClick={() => setMerging(t)}>
            Rename / merge
          </Button>
          <Button size="sm" variant="danger" onClick={() => openDelete(t)}>
            Delete
          </Button>
        </span>
      ),
    },
  ];

  if (tags.isError) {
    const denial = denialOf(tags.error);
    if (denial !== null) return <PolicyGate error={tags.error} />;
    return (
      <Alert tone="danger" title="Could not load tags">
        <p>{messageOf(tags.error)}</p>
        <Button size="sm" onClick={() => void tags.refetch()}>
          Retry
        </Button>
      </Alert>
    );
  }

  return (
    <>
      <div className="page-head">
        <h1>tags</h1>
        <span className="muted">{rows.length} tags</span>
      </div>

      <Card flush>
        <DataTable
          caption="Tags"
          columns={columns}
          rows={rows}
          rowKey={(t) => t.value}
          loading={tags.isPending}
          empty={
            <EmptyState
              title="No tags yet"
              description="Tags come from the challenges — add one on a challenge and it shows up here."
            />
          }
        />
      </Card>

      {merging !== null && (
        <MergeDialog tag={merging} onClose={() => setMerging(null)} />
      )}

      {deleting !== null && (
        <ConfirmDestructive
          open
          onClose={() => setDeleting(null)}
          onConfirm={confirmDelete}
          resourceName={deleting.value}
          resourceKind="tag"
          busy={remove.isPending}
          confirmLabel={force ? "Detach and delete" : "Delete"}
          description={
            force
              ? undefined
              : "The tag is removed from the catalogue. Challenges keep every other tag they carry."
          }
        >
          {deleteError !== null && (
            <Alert tone="warn" title="This tag is still in use">
              <p>{deleteError}</p>
              <p>
                Deleting with <strong>force</strong> detaches <span className="ff-mono">{deleting.value}</span>{" "}
                from the {deleting.uses} challenge{deleting.uses === 1 ? "" : "s"} that still carry it,
                then removes it. The challenges themselves are untouched — they simply lose this one
                tag, and any filter built on it stops matching them.
              </p>
            </Alert>
          )}
        </ConfirmDestructive>
      )}
    </>
  );
}

function MergeDialog({ tag, onClose }: { tag: AdminTag; onClose: () => void }) {
  const toast = useToast();
  const merge = useMergeTag();
  const [into, setInto] = useState(tag.value);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [error, setError] = useState<string | null>(null);

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setErrors({});
    setError(null);
    const target = into.trim();
    merge.mutate(
      { value: tag.value, into: target },
      {
        onSuccess: () => {
          onClose();
          toast.success(`${tag.value} → ${target}`, "Every challenge carrying it now uses the new tag.");
        },
        onError: (failure) => {
          setErrors(fieldErrorsOf(failure));
          setError(messageOf(failure));
        },
      },
    );
  };

  return (
    <Dialog
      open
      size="sm"
      onClose={onClose}
      title="Rename or merge tag"
      description="A new name renames the tag. An existing name merges the two — one tag, one usage count."
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={merge.isPending}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="merge-form"
            variant="primary"
            loading={merge.isPending}
            disabled={into.trim() === "" || into.trim() === tag.value}
          >
            Apply
          </Button>
        </>
      }
    >
      <Form id="merge-form" onSubmit={submit} errors={errors} error={error}>
        <Field name="value" label="From">
          <Input value={tag.value} readOnly mono />
        </Field>
        <Field
          name="into"
          label="Into"
          required
          hint={`Used by ${tag.uses} challenge${tag.uses === 1 ? "" : "s"}.`}
        >
          <Input value={into} mono autoFocus onChange={(e) => setInto(e.target.value)} />
        </Field>
      </Form>
    </Dialog>
  );
}

function fieldErrorsOf(error: unknown): FieldErrors {
  return isApiError(error) ? fieldErrors({ errors: error.fieldErrors }) : {};
}

function messageOf(error: unknown): string {
  if (isApiError(error)) return error.detail;
  return error instanceof Error ? error.message : "unexpected error";
}
