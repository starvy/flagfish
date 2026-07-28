import { Select } from "../ui";
import { useTheme } from "./provider";

// The picker: built-in themes plus a "system" option that follows the OS. Choosing
// one persists it and applies instantly; there is no save button and no reload.
export function ThemeSwitcher() {
  const { themes, preference, setPreference, system, resolved } = useTheme();
  const value = preference ?? system;

  return (
    <>
      <label className="theme-switcher">
        theme
        <Select value={value} onChange={(e) => setPreference(e.target.value)}>
          <option value={system}>system ({resolved.theme.label})</option>
          {themes.map((t) => (
            <option key={t.name} value={t.name}>
              {t.label}
            </option>
          ))}
        </Select>
      </label>

      {/* The choice is saved either way, but it is not what is on screen — say so rather than
          let the picker look broken. */}
      {resolved.source === "view" && (
        <p className="ff-muted">
          the board view you are on brings its own palette ({resolved.theme.label}). switch to the
          list view on the challenges page to see this choice.
        </p>
      )}
    </>
  );
}
