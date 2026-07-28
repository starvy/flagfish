import { useMemo, useState } from "react";
import { Input, Select, type SelectOption } from "../ui";
import { COUNTRY_LIST } from "../globe/data/countryNames";
import { isDrawn } from "../globe/geography";

export interface CountryPickerProps {
  /** The stored ISO 3166-1 alpha-2 code, or null for "not placed". */
  value: string | null;
  onChange: (code: string | null) => void;
  disabled?: boolean;
}

/** The value the "not placed" option carries. Empty string, so it cannot collide with a code. */
const NONE = "";

function matches(query: string, code: string, name: string): boolean {
  if (query === "") return true;
  const q = query.toLowerCase();
  return code.toLowerCase().startsWith(q) || name.toLowerCase().includes(q);
}

/**
 * Picks one country out of 250.
 *
 * A filter box over a native select rather than a custom combobox: the select keeps its own
 * keyboard handling, its mobile picker and its screen-reader behaviour, and the box beside it is
 * the only thing 250 options actually need.
 */
export function CountryPicker({ value, onChange, disabled }: CountryPickerProps) {
  const [query, setQuery] = useState("");

  const options = useMemo<SelectOption[]>(() => {
    const found = COUNTRY_LIST.filter((c) => matches(query, c.code, c.name)).map((c) => ({
      value: c.code,
      // The 1:110m outlines leave out the smallest countries. The annotation is still valid and
      // the challenge is still listed — but nobody should go looking for a pin that cannot exist.
      label: isDrawn(c.code) ? `${c.name} (${c.code})` : `${c.name} (${c.code}) — not on the map`,
    }));
    // The stored value always stays selectable, even when the filter excludes it, so typing in
    // the box cannot silently change what is saved.
    if (value !== null && !found.some((o) => o.value === value)) {
      const known = COUNTRY_LIST.find((c) => c.code === value);
      found.unshift({
        value,
        label: known === undefined ? `${value} (not a country code)` : `${known.name} (${value})`,
      });
    }
    return [{ value: NONE, label: "— not placed —" }, ...found];
  }, [query, value]);

  return (
    <div className="ff-stack">
      <Input
        type="search"
        value={query}
        onChange={(e) => setQuery(e.currentTarget.value)}
        placeholder="filter by name or code"
        aria-label="Filter countries"
        disabled={disabled}
      />
      <Select
        value={value ?? NONE}
        onChange={(e) => onChange(e.currentTarget.value === NONE ? null : e.currentTarget.value)}
        options={options}
        disabled={disabled}
        aria-label="Country"
      />
    </div>
  );
}
