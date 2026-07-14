import { useQuery } from "@tanstack/react-query";
import { instanceQuery } from "../queries";

export interface Clock {
  /** Attempts are refused while paused — admins included. Hint unlocks and browsing keep working. */
  paused: boolean;
  freeze: string | null;
  /** Past the freeze, a solve list is truncated for every viewer without the exemption. */
  frozen: boolean;
}

export function useClock(): Clock {
  const { data } = useQuery(instanceQuery);
  const freeze = data?.freeze ?? null;

  return {
    paused: data?.paused ?? false,
    freeze,
    frozen: freeze !== null && Date.now() >= Date.parse(freeze),
  };
}
