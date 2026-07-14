import { useState } from "react";
import { useMutation, useQuery, useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { api, ApiError } from "../../api/client";
import type { AttemptResult, ChallengeDetail } from "../../api/client";
import { challengeQuery, challengesQuery, scoreboardQuery, solvesQuery } from "../../queries";

export const Route = createFileRoute("/_auth/challenges/$challengeId")({
  params: {
    parse: (raw) => ({ challengeId: Number(raw.challengeId) }),
    stringify: (params) => ({ challengeId: String(params.challengeId) }),
  },
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(challengeQuery(params.challengeId)),
  component: ChallengeDialog,
});

function ChallengeDialog() {
  const { challengeId } = Route.useParams();
  const { data: chal } = useSuspenseQuery(challengeQuery(challengeId));
  const navigate = useNavigate();

  const close = () => void navigate({ to: "/challenges" });

  return (
    <div
      className="overlay"
      onClick={(e) => {
        if (e.target === e.currentTarget) close();
      }}
    >
      <div className="dialog" role="dialog" aria-label={chal.name}>
        <header>
          <h2>{chal.name}</h2>
          <span className="points">{chal.value} pts</span>
          <button className="close" onClick={close} aria-label="close">
            ✕
          </button>
        </header>
        <p className="muted">
          {chal.category} · {chal.solve_count} solves
          {chal.solved && <span className="ok"> · solved ✓</span>}
        </p>
        {(chal.tags ?? []).length > 0 && (
          <div className="tags">
            {(chal.tags ?? []).map((t) => (
              <span className="tag" key={t}>
                {t}
              </span>
            ))}
          </div>
        )}
        <p className="description">{chal.description}</p>
        {chal.connection_info && (
          <p>
            <code>{chal.connection_info}</code>
          </p>
        )}
        {chal.attribution && <p className="muted">{chal.attribution}</p>}

        {(chal.files ?? []).length > 0 && (
          <section>
            <h3>files</h3>
            <ul className="file-list">
              {(chal.files ?? []).map((f) => (
                <li key={f.id}>
                  <a href={f.location} download>
                    {f.location.split("/").pop() ?? f.location}
                  </a>{" "}
                  <span className="muted">({formatSize(f.size_bytes)})</span>
                </li>
              ))}
            </ul>
          </section>
        )}

        {(chal.hints ?? []).length > 0 && <Hints chal={chal} />}

        <section>
          <h3>flag</h3>
          <FlagForm chal={chal} />
        </section>

        <Solvers challengeId={challengeId} />
      </div>
    </div>
  );
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}

function Hints({ chal }: { chal: ChallengeDetail }) {
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState<number | null>(null);

  const unlock = useMutation({
    mutationFn: (hintId: number) => api.unlockHint(chal.id, hintId),
    onSuccess: async (result) => {
      // Hint content only ever arrives in the unlock response, so keep it around.
      queryClient.setQueryData(["hint-content", result.hint_id], result.content);
      await queryClient.invalidateQueries({ queryKey: ["challenges", chal.id] });
    },
    onSettled: () => setConfirming(null),
  });

  return (
    <section>
      <h3>hints</h3>
      <ul className="hint-list">
        {(chal.hints ?? []).map((h) => (
          <HintRow
            key={h.id}
            hint={h}
            confirming={confirming === h.id}
            onAsk={() => setConfirming(h.id)}
            onCancel={() => setConfirming(null)}
            onConfirm={() => unlock.mutate(h.id)}
            pending={unlock.isPending && unlock.variables === h.id}
          />
        ))}
      </ul>
      {unlock.error && (
        <p className="error">
          {unlock.error instanceof ApiError ? unlock.error.message : "unlock failed"}
        </p>
      )}
    </section>
  );
}

function HintRow(props: {
  hint: NonNullable<ChallengeDetail["hints"]>[number];
  confirming: boolean;
  pending: boolean;
  onAsk: () => void;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const { hint } = props;
  const { data: content } = useQuery({
    queryKey: ["hint-content", hint.id],
    queryFn: () => Promise.resolve<string | null>(null),
    enabled: false, // written by the unlock mutation, never fetched
  });

  const title = hint.title ?? `hint #${hint.id}`;

  if (hint.unlocked) {
    return (
      <li>
        <div className="hint-row">
          <span className="grow">{title}</span>
          <span className="muted">unlocked</span>
        </div>
        <div className="hint-content">
          {content ?? <span className="muted">unlocked in an earlier session</span>}
        </div>
      </li>
    );
  }

  return (
    <li className="hint-row">
      <span className="grow">{title}</span>
      {props.confirming ? (
        <>
          <button onClick={props.onConfirm} disabled={props.pending}>
            confirm −{hint.cost} pts
          </button>
          <button className="ghost" onClick={props.onCancel} disabled={props.pending}>
            cancel
          </button>
        </>
      ) : (
        <button className="ghost" onClick={props.onAsk}>
          unlock ({hint.cost} pts)
        </button>
      )}
    </li>
  );
}

function FlagForm({ chal }: { chal: ChallengeDetail }) {
  const queryClient = useQueryClient();
  const [flag, setFlag] = useState("");
  const [verdict, setVerdict] = useState<AttemptResult | null>(null);

  const attempt = useMutation({
    mutationFn: () => api.attempt(chal.id, flag),
    onSuccess: async (result) => {
      setVerdict(result);
      if (result.status === "correct") {
        setFlag("");
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: challengesQuery.queryKey }),
          queryClient.invalidateQueries({ queryKey: challengeQuery(chal.id).queryKey }),
          queryClient.invalidateQueries({ queryKey: solvesQuery(chal.id).queryKey }),
          queryClient.invalidateQueries({ queryKey: scoreboardQuery.queryKey }),
        ]);
      }
    },
    onError: () => setVerdict(null),
  });

  return (
    <>
      <form
        className="flag-form"
        onSubmit={(e) => {
          e.preventDefault();
          if (flag.trim() !== "") attempt.mutate();
        }}
      >
        <input
          placeholder="flag{…}"
          value={flag}
          onChange={(e) => setFlag(e.target.value)}
          spellCheck={false}
          autoComplete="off"
        />
        <button type="submit" disabled={attempt.isPending || flag.trim() === ""}>
          {attempt.isPending ? "…" : "submit"}
        </button>
      </form>
      {attempt.error && (
        <p className="error">
          {attempt.error instanceof ApiError ? attempt.error.message : "submission failed"}
        </p>
      )}
      {verdict && <Verdict verdict={verdict} />}
    </>
  );
}

function Verdict({ verdict }: { verdict: AttemptResult }) {
  switch (verdict.status) {
    case "correct":
      return (
        <div className="verdict correct">
          {verdict.first_blood && <div className="first-blood">🩸 first blood</div>}
          correct — +{verdict.value} pts
        </div>
      );
    case "already_solved":
      return <div className="verdict already_solved">already solved</div>;
    default:
      return <div className="verdict incorrect">incorrect — keep digging</div>;
  }
}

function Solvers({ challengeId }: { challengeId: number }) {
  const { data } = useQuery(solvesQuery(challengeId));
  const solves = data?.solves ?? [];
  if (solves.length === 0) return null;

  return (
    <section>
      <h3>solved by</h3>
      <ul className="solver-list">
        {solves.map((s, i) => (
          <li key={`${s.name}-${s.date}`}>
            {s.name} {i === 0 && <span className="first-blood">🩸</span>}{" "}
            <span className="muted">{new Date(s.date).toLocaleString()}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}
