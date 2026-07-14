import { useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { notificationsQuery, useCreateNotification } from "../../../queries";
import { ApiError } from "../../../api/errors";
import { denialOf, PolicyGate } from "../../../policy";
import {
  Alert,
  Button,
  Card,
  DataTable,
  Dialog,
  EmptyState,
  Field,
  Form,
  Input,
  Markdown,
  RelativeTime,
  Textarea,
  useToast,
  type Column,
  type FieldErrors,
} from "../../../ui";

export const Route = createFileRoute("/_auth/admin/notifications")({
  component: NotificationsPage,
});

interface Published {
  id: number;
  title: string;
  content: string;
  date: string;
}

const RECENT: Column<Published>[] = [
  {
    key: "title",
    header: "title",
    cell: (n) => <span className="ff-truncate">{n.title}</span>,
    width: "30%",
  },
  {
    key: "content",
    header: "body",
    cell: (n) => <span className="ff-truncate muted">{oneLine(n.content)}</span>,
  },
  {
    key: "date",
    header: "published",
    align: "right",
    width: "10rem",
    cell: (n) => <RelativeTime value={n.date} />,
  },
];

function NotificationsPage() {
  const toast = useToast();
  const [title, setTitle] = useState("");
  const [content, setContent] = useState("");
  const [confirming, setConfirming] = useState(false);

  const publish = useCreateNotification();
  const recent = useQuery({ ...notificationsQuery({ page: 1, per_page: 10 }), staleTime: 0 });

  const ready = title.trim() !== "" && content.trim() !== "";

  const onSubmit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (ready) setConfirming(true);
  };

  const send = () => {
    publish.mutate(
      { title: title.trim(), content },
      {
        onSuccess: (n) => {
          setConfirming(false);
          setTitle("");
          setContent("");
          toast.success("Published", `“${n.title}” went out to every connected client.`);
        },
        onError: (e) => {
          setConfirming(false);
          toast.error("Not published", messageOf(e));
        },
      },
    );
  };

  const denial = recent.error === null ? null : denialOf(recent.error);
  if (denial !== null) return <PolicyGate error={recent.error} />;

  return (
    <div className="ff-stack">
      <div className="page-head">
        <h1>notifications</h1>
      </div>

      <Alert tone="warn" title="Publishing is a broadcast, not a draft">
        The moment you publish, this notification is pushed down the live stream to every player
        who has the site open, and it lands in the history for everyone else. There is no edit and
        no unsend.
      </Alert>

      <Card title="Compose">
        <Form
          onSubmit={onSubmit}
          errors={fieldMessages(publish.error)}
          error={formMessage(publish.error)}
          footer={
            <Button type="submit" variant="primary" disabled={!ready} loading={publish.isPending}>
              Publish to everyone
            </Button>
          }
        >
          <Field name="title" label="Title" required>
            <Input
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              maxLength={256}
              placeholder="Challenge 'sudoku' has been fixed"
            />
          </Field>

          <div
            style={{
              display: "grid",
              gap: "var(--space-4)",
              gridTemplateColumns: "repeat(auto-fit, minmax(20rem, 1fr))",
            }}
          >
            <Field
              name="content"
              label="Body"
              hint="Markdown. Links, code, lists and emphasis; no raw HTML."
              required
            >
              <Textarea
                value={content}
                onChange={(e) => setContent(e.target.value)}
                rows={14}
                mono
                placeholder={"The flag format is `flagfish{...}`.\n\nSorry for the noise."}
              />
            </Field>

            <div className="ff-field">
              <span className="ff-field__label">Preview</span>
              <div className="ff-card__body">
                {content.trim() === "" ? (
                  <p className="muted">What you type renders here, exactly as a player sees it.</p>
                ) : (
                  <Markdown source={content} />
                )}
              </div>
            </div>
          </div>
        </Form>
      </Card>

      <Card title="Recently published" flush>
        {recent.isError ? (
          <div className="ff-card__body">
            <Alert tone="danger" title="Could not load the history">
              <p>{messageOf(recent.error)}</p>
              <Button size="sm" onClick={() => void recent.refetch()}>
                Retry
              </Button>
            </Alert>
          </div>
        ) : (
          <DataTable
            caption="Recently published notifications"
            columns={RECENT}
            rows={recent.data?.notifications ?? []}
            rowKey={(n) => n.id}
            loading={recent.isPending}
            empty={
              <EmptyState
                title="Nothing has been published"
                description="Notifications you publish appear here, newest first."
              />
            }
          />
        )}
      </Card>

      <Dialog
        open={confirming}
        onClose={() => setConfirming(false)}
        title="Publish to every connected client?"
        description="This fans out immediately over the live stream. It cannot be edited or unsent."
        footer={
          <>
            <Button variant="ghost" onClick={() => setConfirming(false)} disabled={publish.isPending}>
              Cancel
            </Button>
            <Button variant="primary" onClick={send} loading={publish.isPending}>
              Publish
            </Button>
          </>
        }
      >
        <div className="ff-stack">
          <strong>{title}</strong>
          <Markdown source={content} />
        </div>
      </Dialog>
    </div>
  );
}

function oneLine(s: string): string {
  return s.replace(/\s+/g, " ").trim();
}

function messageOf(e: unknown): string {
  return e instanceof ApiError ? e.detail : "Something went wrong.";
}

/** A 422 names the offending field; anything else belongs above the form, not under a label. */
function fieldMessages(e: unknown): FieldErrors {
  if (!(e instanceof ApiError)) return {};
  const out: FieldErrors = {};
  for (const f of e.fieldErrors) {
    const leaf = f.location?.split(".").pop()?.replace(/\[\d+\]$/, "") ?? "";
    if (leaf !== "" && leaf !== "body" && !(leaf in out)) out[leaf] = f.message;
  }
  return out;
}

function formMessage(e: unknown): string | null {
  if (!(e instanceof ApiError)) return e == null ? null : "Something went wrong.";
  return e.fieldErrors.length > 0 ? null : e.detail;
}
