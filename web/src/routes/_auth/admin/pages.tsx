import { useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import type { components } from "../../../api/schema.admin.gen";
import { isApiError } from "../../../api/errors";
import { denialOf, PolicyGate } from "../../../policy";
import {
  adminPageQuery,
  adminPagesQuery,
  useCreatePage,
  useDeletePage,
  useUpdatePage,
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
  Markdown,
  Textarea,
  useToast,
  type Column,
  type FieldErrors,
} from "../../../ui";

type PageRow = components["schemas"]["AdminPageListItem"];

export const Route = createFileRoute("/_auth/admin/pages")({
  component: PagesPage,
});

function PagesPage() {
  const toast = useToast();

  const pages = useQuery(adminPagesQuery);
  const rows = pages.data?.pages ?? [];

  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<PageRow | null>(null);
  const [deleting, setDeleting] = useState<PageRow | null>(null);

  const create = useCreatePage();
  const update = useUpdatePage();
  const remove = useDeletePage();

  const columns: readonly Column<PageRow>[] = [
    { key: "title", header: "title", cell: (p) => p.title },
    { key: "route", header: "route", cell: (p) => <code>/{p.route}</code> },
    {
      key: "status",
      header: "status",
      cell: (p) => (
        <span className="ff-row">
          <Badge tone={p.draft ? "warn" : "success"}>{p.draft ? "draft" : "published"}</Badge>
          {p.auth_required && <Badge tone="neutral">members</Badge>}
        </span>
      ),
    },
    {
      key: "actions",
      header: "Actions",
      headerHidden: true,
      align: "right",
      cell: (p) => (
        <span className="ff-row">
          <Button size="sm" variant="ghost" onClick={() => setEditing(p)}>
            Edit
          </Button>
          <Button size="sm" variant="danger" onClick={() => setDeleting(p)}>
            Delete
          </Button>
        </span>
      ),
    },
  ];

  if (pages.isError) {
    const denial = denialOf(pages.error);
    if (denial !== null) return <PolicyGate error={pages.error} />;
    return (
      <Alert tone="danger" title="Could not load pages">
        <p>{messageOf(pages.error)}</p>
        <Button size="sm" onClick={() => void pages.refetch()}>
          Retry
        </Button>
      </Alert>
    );
  }

  const confirmDelete = () => {
    if (deleting === null) return;
    remove.mutate(deleting.id, {
      onSuccess: () => {
        const title = deleting.title;
        setDeleting(null);
        toast.success(`Deleted ${title}`);
      },
      onError: (error) => toast.error("Could not delete the page", messageOf(error)),
    });
  };

  return (
    <>
      <div className="page-head">
        <h1>pages</h1>
        <Button variant="primary" onClick={() => setCreating(true)}>
          New page
        </Button>
      </div>

      <Card flush>
        <DataTable
          caption="Pages"
          columns={columns}
          rows={rows}
          rowKey={(p) => p.id}
          loading={pages.isPending}
          empty={
            <EmptyState
              title="No pages yet"
              description="A page publishes rules, an FAQ or sponsors — markdown, served at its own URL."
              action={
                <Button variant="primary" onClick={() => setCreating(true)}>
                  New page
                </Button>
              }
            />
          }
        />
      </Card>

      {creating && (
        <PageDialog
          busy={create.isPending}
          onClose={() => setCreating(false)}
          onSubmit={(body, onError) =>
            create.mutate(body, {
              onSuccess: (page) => {
                setCreating(false);
                toast.success(
                  `Created ${page.title}`,
                  page.draft ? "It is a draft until you publish it." : `Live at /${page.route}.`,
                );
              },
              onError,
            })
          }
        />
      )}

      {editing !== null && (
        <EditPageDialog
          id={editing.id}
          busy={update.isPending}
          onClose={() => setEditing(null)}
          onSubmit={(body, onError) =>
            update.mutate(
              { id: editing.id, body },
              {
                onSuccess: (page) => {
                  setEditing(null);
                  toast.success(`Saved ${page.title}`);
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
          resourceName={deleting.title}
          resourceKind="page"
          busy={remove.isPending}
          description="The page and its content are removed. This cannot be undone."
        />
      )}
    </>
  );
}

interface PageForm {
  route: string;
  title: string;
  content: string;
  draft: boolean;
  auth_required: boolean;
}

// EditPageDialog fetches the full body first: the admin list omits content, so an editor opened from
// it would otherwise blank the page's text on save.
function EditPageDialog({
  id,
  busy,
  onClose,
  onSubmit,
}: {
  id: number;
  busy: boolean;
  onClose: () => void;
  onSubmit: (body: PageForm, onError: (error: unknown) => void) => void;
}) {
  const page = useQuery(adminPageQuery(id));

  if (page.isError) {
    return (
      <Dialog open onClose={onClose} title="Edit page">
        <PolicyGate error={page.error}>
          <Alert tone="danger" title="Could not load the page">
            {messageOf(page.error)}
          </Alert>
        </PolicyGate>
      </Dialog>
    );
  }
  if (page.data === undefined) {
    return (
      <Dialog open onClose={onClose} title="Edit page">
        <p className="ff-muted">Loading…</p>
      </Dialog>
    );
  }

  return (
    <PageDialog
      page={page.data}
      busy={busy}
      onClose={onClose}
      onSubmit={onSubmit}
    />
  );
}

interface PageDialogProps {
  page?: {
    route: string;
    title: string;
    content: string;
    draft: boolean;
    auth_required: boolean;
  };
  busy: boolean;
  onClose: () => void;
  onSubmit: (body: PageForm, onError: (error: unknown) => void) => void;
}

function PageDialog({ page, busy, onClose, onSubmit }: PageDialogProps) {
  const editing = page !== undefined;
  const [route, setRoute] = useState(page?.route ?? "");
  const [title, setTitle] = useState(page?.title ?? "");
  const [content, setContent] = useState(page?.content ?? "");
  const [draft, setDraft] = useState(page?.draft ?? true);
  const [authRequired, setAuthRequired] = useState(page?.auth_required ?? false);
  const [preview, setPreview] = useState(false);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [error, setError] = useState<string | null>(null);

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setErrors({});
    setError(null);
    onSubmit(
      {
        route: route.trim(),
        title: title.trim(),
        content,
        draft,
        auth_required: authRequired,
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
      size="lg"
      title={editing ? "Edit page" : "New page"}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" form="page-form" variant="primary" loading={busy}>
            {editing ? "Save" : "Create"}
          </Button>
        </>
      }
    >
      <Form id="page-form" onSubmit={submit} errors={errors} error={error}>
        <Field
          name="route"
          label="Route"
          required
          hint="The URL slug: lowercase letters, digits and hyphens. Served at /{route}."
        >
          <Input
            value={route}
            mono
            onChange={(e) => setRoute(e.target.value)}
            maxLength={64}
            placeholder="rules"
          />
        </Field>

        <Field name="title" label="Title" required>
          <Input value={title} onChange={(e) => setTitle(e.target.value)} maxLength={200} />
        </Field>

        <Field
          name="content"
          label="Content"
          hint="Markdown. It is rendered in the browser — the server stores it verbatim."
        >
          {preview ? (
            <Card>
              <Markdown source={content} />
            </Card>
          ) : (
            <Textarea
              value={content}
              mono
              rows={14}
              onChange={(e) => setContent(e.target.value)}
              maxLength={262144}
            />
          )}
        </Field>

        <div className="ff-row">
          <Button size="sm" variant="ghost" type="button" onClick={() => setPreview((p) => !p)}>
            {preview ? "Edit" : "Preview"}
          </Button>
        </div>

        <Field
          name="draft"
          label="Publication"
          hint="A draft is invisible to the public until you publish it."
        >
          <Checkbox
            checked={!draft}
            onChange={(e) => setDraft(!e.target.checked)}
            label="Published"
          />
        </Field>

        <Field
          name="auth_required"
          label="Audience"
          hint="Members-only pages ask an anonymous visitor to sign in first."
        >
          <Checkbox
            checked={authRequired}
            onChange={(e) => setAuthRequired(e.target.checked)}
            label="Signed-in players only"
          />
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
