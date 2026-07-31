import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { challengeQuery } from "../queries";
import { Alert, Badge, Card, CodeBlock, Markdown, Skeleton, Tabs } from "../ui";
import { Attachments } from "./Attachments";
import { ClockBanners } from "./Banners";
import { FlagForm } from "./FlagForm";
import { HintList } from "./HintList";
import { QueryError } from "./QueryError";
import { SolvesTab } from "./SolvesTab";
import { useClock } from "./clock";
import { solveCountLabel, type Challenge } from "./types";
import "./challenges.css";

export interface ChallengeDetailBodyProps {
  challengeId: number;
  /**
   * The detail is a page on `/challenges/$id` and a panel inside a view's drawer. Only the two
   * things that belong to a page — the way back and the top-level heading — differ.
   */
  showBackLink?: boolean;
  headingLevel?: 1 | 2;
}

/**
 * One challenge, everything a player can do with it: read it, take its files and hints, and
 * submit against it. It owns its own read so a host only has to know the id.
 */
export function ChallengeDetailBody({
  challengeId,
  showBackLink = true,
  headingLevel = 1,
}: ChallengeDetailBodyProps) {
  const detail = useQuery(challengeQuery(challengeId));
  const { paused } = useClock();

  if (detail.isPending) return <DetailSkeleton />;
  if (detail.isError) {
    return (
      <QueryError
        error={detail.error}
        onRetry={() => void detail.refetch()}
        title="could not load this challenge"
      />
    );
  }

  const challenge: Challenge = detail.data;
  const Heading = headingLevel === 2 ? "h2" : "h1";

  return (
    <>
      {showBackLink && (
        <Link to="/challenges" className="chal-detail__back">
          ← all challenges
        </Link>
      )}

      <header className="chal-detail__head">
        <div className="chal-detail__title">
          <Heading>{challenge.name}</Heading>
          <span className="chal-detail__value">{challenge.value} pts</span>
        </div>
        <div className="chal-detail__meta">
          <span>{challenge.category}</span>
          <span>·</span>
          <span title={challenge.solve_count === null ? "solve counts are hidden on this instance" : undefined}>
            {solveCountLabel(challenge.solve_count)}
          </span>
          {challenge.solved && <Badge tone="success">solved</Badge>}
          {challenge.solved && challenge.next_id != null && (
            <Link
              to="/challenges/$challengeId"
              params={{ challengeId: challenge.next_id }}
              className="chal-detail__next"
            >
              next challenge →
            </Link>
          )}
          {challenge.locked && <Badge tone="warn">locked</Badge>}
          {challenge.flag_mode === "unique" && <Badge tone="info">unique flag</Badge>}
          {(challenge.tags ?? []).map((tag) => (
            <Badge key={tag} tone="neutral">
              {tag}
            </Badge>
          ))}
        </div>
      </header>

      <ClockBanners />

      {challenge.locked && (
        <Alert tone="warn" title="locked">
          this challenge has prerequisites you have not solved yet. you can read it, but a flag will
          not be judged until they are done.
        </Alert>
      )}

      <Tabs
        label="challenge"
        items={[
          { id: "challenge", label: "challenge", content: <Brief challenge={challenge} paused={paused} /> },
          { id: "solves", label: "solves", content: <SolvesTab challengeId={challenge.id} /> },
        ]}
      />
    </>
  );
}

function Brief({ challenge, paused }: { challenge: Challenge; paused: boolean }) {
  const files = challenge.files ?? [];

  return (
    <div className="ff-stack">
      {/* Operator-authored, so it is rendered through the sanitizing Markdown primitive. */}
      <Markdown source={challenge.description} />

      {challenge.connection_info !== undefined && (
        <CodeBlock title="connection" code={challenge.connection_info} wrap />
      )}

      {challenge.instance !== undefined && (
        <CodeBlock
          title="your instance"
          language="json"
          code={JSON.stringify(challenge.instance.vars, null, 2)}
          wrap
        />
      )}

      {challenge.attribution !== undefined && (
        <p className="muted chal-detail__attribution">{challenge.attribution}</p>
      )}

      <section className="chal-detail__section">
        <h3>files</h3>
        <Attachments files={files} />
      </section>

      <section className="chal-detail__section">
        <h3>hints</h3>
        <HintList challenge={challenge} />
      </section>

      <Card title="submit a flag">
        <FlagForm challenge={challenge} paused={paused} />
      </Card>
    </div>
  );
}

function DetailSkeleton() {
  return (
    <div className="ff-stack">
      <Skeleton width="14rem" height="2rem" />
      <Skeleton width="20rem" height="1rem" />
      <Skeleton lines={6} height="1rem" />
      <Skeleton height="3rem" />
    </div>
  );
}
