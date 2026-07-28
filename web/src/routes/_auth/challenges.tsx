import { Outlet, createFileRoute, useChildMatches } from "@tanstack/react-router";
import { Board } from "../../challenges/Board";

export const Route = createFileRoute("/_auth/challenges")({
  component: ChallengesRoute,
});

// `/challenges/$id` nests under this route in the file tree, but it is a page of its own, not a
// panel inside the board. When a child matches, the board steps out of its way.
function ChallengesRoute() {
  const children = useChildMatches();
  return children.length > 0 ? <Outlet /> : <Board />;
}
