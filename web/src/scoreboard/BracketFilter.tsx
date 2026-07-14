import type { Brackets } from "../api/client";
import { Select } from "../ui";

export type Bracket = NonNullable<Brackets["brackets"]>[number];

export interface BracketFilterProps {
  brackets: readonly Bracket[];
  /** Undefined is the overall board — no bracket at all, not "bracket zero". */
  value: number | undefined;
  onChange: (bracket: number | undefined) => void;
}

export function BracketFilter({ brackets, value, onChange }: BracketFilterProps) {
  return (
    <label className="ff-board-control">
      <span className="ff-board-control__label">bracket</span>
      <Select
        value={value === undefined ? "" : String(value)}
        onChange={(e) => onChange(e.target.value === "" ? undefined : Number(e.target.value))}
      >
        <option value="">overall</option>
        {brackets.map((b) => (
          <option key={b.id} value={b.id} title={b.description}>
            {b.name}
          </option>
        ))}
      </Select>
    </label>
  );
}
