import { useState } from "react";
import { isApiError } from "../api/client";
import { useDownloadFile } from "../queries";
import { Button, EmptyState, useToast } from "../ui";
import { fileSize, type ChallengeFile } from "./types";

/**
 * Challenge attachments.
 *
 * `download-file` answers with bytes and a Content-Disposition, not a URL — the route is gated
 * exactly like the challenge itself, so a plain <a href> would fetch it without the session's
 * error handling and hand the player a saved copy of a problem document. The blob is fetched
 * through the client and saved under the name the server chose.
 */
export function Attachments({ files }: { files: ChallengeFile[] }) {
  const download = useDownloadFile();
  const toast = useToast();
  const [busy, setBusy] = useState<number | null>(null);

  if (files.length === 0) {
    return <EmptyState title="no files" description="nothing to download for this one." />;
  }

  const save = (file: ChallengeFile) => {
    setBusy(file.id);
    download.mutate(file.id, {
      onSuccess: ({ blob, filename }) => saveBlob(blob, filename ?? file.name),
      onError: (error) =>
        toast.error(
          "download failed",
          isApiError(error) ? error.detail : "the file did not reach you.",
        ),
      onSettled: () => setBusy(null),
    });
  };

  return (
    <ul className="file-list">
      {files.map((file) => (
        <li key={file.id} className="file-list__row">
          <span className="file-list__name">{file.name}</span>
          <span className="muted">{fileSize(file.size_bytes)}</span>
          <span className="ff-spacer" />
          <Button
            size="sm"
            variant="secondary"
            loading={busy === file.id}
            onClick={() => save(file)}
          >
            download
          </Button>
        </li>
      ))}
    </ul>
  );
}

function saveBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.append(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}
</content>
