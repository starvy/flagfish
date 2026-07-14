import { useQuery } from "@tanstack/react-query";
import { instanceQuery } from "../queries";
import { asInstanceInfo } from "./types";

export interface Clock {
  /** Attempts are refused while paused — for admins too. Hint unlocks and browsing keep working. */
  paused: boolean;
  freeze: string | null;
  /** Past the freeze: every solve list and standing is truncated for a viewer without the exemption. */
  frozen: boolean;
}

export function useClock(): Clock {
  const { data } = useQuery(instanceQuery);
  const instance = data === undefined ? null : asInstanceInfo(data);
  const freeze = instance?.freeze ?? null;

  return {
    paused: instance?.paused ?? false,
    freeze,
    frozen: freeze !== null && Date.now() >= Date.parse(freeze),
  };
}
</content>
</invoke>
