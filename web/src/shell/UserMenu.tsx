import { useEffect, useId, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import type { Me } from "../api/client";
import { myTeamQuery, scoreboardQuery, useLogout } from "../queries";
import { Button } from "../ui";
import { useInstanceState } from "./instance";

/**
 * The caller's own score, or null when this instance will not show it.
 *
 * Scores can be hidden or admins-only, and that denial is an answer, not a fault: the menu
 * simply omits the number. In teams mode the score that matters is the team's.
 */
function useMyScore(me: Me): number | null {
  const { teamsMode } = useInstanceState();

  const team = useQuery({ ...myTeamQuery, enabled: teamsMode });
  // The board's own key, so on the scoreboard this costs nothing; elsewhere it is a slow poll.
  const board = useQuery({
    ...scoreboardQuery(),
    enabled: !teamsMode,
    refetchInterval: 60_000,
    retry: false,
  });

  if (teamsMode) return team.data?.score ?? null;
  const mine = board.data?.standings.find((row) => row.account_id === me.user_id);
  return mine?.score ?? null;
}

export function UserMenu({ me }: { me: Me }) {
  const [open, setOpen] = useState(false);
  const menuId = useId();
  const root = useRef<HTMLDivElement>(null);
  const navigate = useNavigate();
  const logout = useLogout();
  const score = useMyScore(me);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    const onPointer = (e: PointerEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("pointerdown", onPointer);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("pointerdown", onPointer);
    };
  }, [open]);

  const signOut = () => {
    logout.mutate(undefined, {
      // The cache belongs to the session that is ending, and useLogout has already dropped it;
      // settled, not success, because a failed logout still means we are done with it here.
      onSettled: () => void navigate({ to: "/login", search: { redirect: undefined } }),
    });
  };

  return (
    <div className="sh-usermenu" ref={root}>
      <button
        type="button"
        className="sh-usermenu__trigger"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => setOpen((v) => !v)}
      >
        <span className="sh-usermenu__name">{me.name}</span>
        {score !== null && (
          <span className="sh-usermenu__score" title="points">
            {score}
          </span>
        )}
      </button>

      {open && (
        <div className="sh-usermenu__menu" id={menuId} role="menu">
          <div className="sh-usermenu__head">
            <span className="sh-usermenu__name">{me.name}</span>
            <span className="ff-muted">{me.email}</span>
          </div>
          <Link
            to="/settings"
            role="menuitem"
            className="sh-usermenu__item"
            onClick={() => setOpen(false)}
          >
            settings
          </Link>
          <div className="sh-usermenu__foot">
            <Button
              variant="ghost"
              size="sm"
              block
              role="menuitem"
              onClick={signOut}
              loading={logout.isPending}
            >
              log out
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
