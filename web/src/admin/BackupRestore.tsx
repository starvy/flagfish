import { useRef, useState, type ChangeEvent } from "react";
import { adminApi, type AdminTask } from "../api/admin";
import { isApiError } from "../api/client";
import { Alert, Badge, Button, Card } from "../ui";
import { errorDetail } from "./errors";

const TERMINAL = new Set(["succeeded", "failed", "cancelled"]);
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// The card that makes backup, restore and import reachable without a shell on the box. Each action
// enqueues a task server-side and returns immediately; this polls the task until it settles, so a
// long restore shows a live bar instead of a frozen page. Only one operation of a kind runs at a
// time — the server answers a second with 409, which reads here as "already running".
export function BackupRestore() {
  const [task, setTask] = useState<AdminTask | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const restoreInput = useRef<HTMLInputElement>(null);
  const importInput = useRef<HTMLInputElement>(null);

  const running = busy || (task !== null && !TERMINAL.has(task.state));

  async function run(start: () => Promise<AdminTask>) {
    setError(null);
    setBusy(true);
    setTask(null);
    try {
      let current = await start();
      setTask(current);
      while (!TERMINAL.has(current.state)) {
        await sleep(1200);
        current = await adminApi.getTask(current.id);
        setTask(current);
      }
    } catch (e) {
      // A 409 is the single-in-flight refusal, not a failure of this attempt.
      if (isApiError(e) && e.status === 409) {
        setError("Another backup or restore is already running. Wait for it to finish.");
      } else {
        setError(errorDetail(e));
      }
    } finally {
      setBusy(false);
    }
  }

  const onFile = (start: (f: File) => Promise<AdminTask>) => (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = ""; // let the same file be chosen again after a failure
    if (file) void run(() => start(file));
  };

  const done = task !== null && task.state === "succeeded";
  const failed = task !== null && task.state === "failed";

  return (
    <Card title="Backup & restore">
      <p className="muted">
        A full backup is the restorable disaster-recovery archive (password hashes and flags
        included); a shareable one is field-masked and safe to hand out. Restore replaces the whole
        instance from a full backup; import loads a foreign CTFd archive. One at a time.
      </p>

      {error !== null && (
        <Alert tone="danger" title="Could not run">
          {error}
        </Alert>
      )}

      <nav className="admin-links" aria-label="Backup">
        <Button
          variant="primary"
          size="sm"
          loading={busy}
          disabled={running}
          onClick={() => void run(() => adminApi.startBackup("backup"))}
        >
          Back up (full)
        </Button>
        <Button
          variant="secondary"
          size="sm"
          disabled={running}
          onClick={() => void run(() => adminApi.startBackup("safe"))}
        >
          Back up (shareable)
        </Button>
        <Button
          variant="secondary"
          size="sm"
          disabled={running}
          onClick={() => restoreInput.current?.click()}
        >
          Restore a backup…
        </Button>
        <Button
          variant="secondary"
          size="sm"
          disabled={running}
          onClick={() => importInput.current?.click()}
        >
          Import a CTFd archive…
        </Button>
      </nav>

      {/* Uploads go straight to the API as multipart; the hidden inputs are the file pickers the
          buttons above drive. */}
      <input
        ref={restoreInput}
        type="file"
        accept=".zip,application/zip"
        hidden
        onChange={onFile((f) => adminApi.startRestore(f))}
      />
      <input
        ref={importInput}
        type="file"
        accept=".zip,application/zip"
        hidden
        onChange={onFile((f) => adminApi.startImport(f))}
      />

      {task !== null && (
        <div className="ff-stack" style={{ marginTop: "1rem" }}>
          <div className="ff-row">
            <Badge tone={failed ? "warn" : done ? "accent" : "neutral"}>{task.state}</Badge>
            <span className="muted">{task.kind === "export" ? "backup" : task.kind}</span>
          </div>
          <progress value={task.progress} max={100} style={{ width: "100%" }} />
          {task.detail !== undefined && task.detail !== "" && (
            <p className="muted">{task.detail}</p>
          )}
          {task.error !== undefined && task.error !== "" && (
            <Alert tone="danger" title="Operation failed">
              {task.error}
            </Alert>
          )}
          {done && task.download !== undefined && task.download !== "" && (
            <p>
              {/* A plain anchor: the archive is a file download served by the API under the same
                  admin session cookie, which a client-side navigation would swallow. */}
              <a href={task.download}>Download backup</a>
            </p>
          )}
        </div>
      )}
    </Card>
  );
}
