import { createFileRoute } from "@tanstack/react-router";
import { ChallengeDetailBody } from "../../challenges/ChallengeDetailBody";

export const Route = createFileRoute("/_auth/challenges/$challengeId")({
  params: {
    parse: (raw) => ({ challengeId: Number(raw.challengeId) }),
    stringify: (params) => ({ challengeId: String(params.challengeId) }),
  },
  component: ChallengePage,
});

function ChallengePage() {
  const { challengeId } = Route.useParams();
  return <ChallengeDetailBody challengeId={challengeId} />;
}
