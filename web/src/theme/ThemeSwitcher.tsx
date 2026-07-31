import { Select } from "../ui";
import { DEFAULT_VIEW, useResolvedPortalView } from "../views";
import { useTheme } from "./provider";

const NOTE_ID = "theme-view-note";

// The picker: built-in themes plus a "system" option that follows the OS. Choosing
// one persists it and applies instantly; there is no save button and no reload.
export function ThemeSwitcher() {
  const { themes, preference, setPreference, system, resolved, viewOverrides } = useTheme();
  const view = useResolvedPortalView();
  const value = preference ?? system;

  return (
    <>
      <label className="theme-switcher">
        theme
        <Select
          value={value}
          onChange={(e) => setPreference(e.target.value)}
          aria-describedby={viewOverrides ? NOTE_ID : undefined}
        >
          <option value={system}>system ({resolved.theme.label})</option>
          {themes.map((t) => (
            <option key={t.name} value={t.name}>
              {t.label}
            </option>
          ))}
        </Select>
      </label>

      {/* Only when the view is painting something else than this choice would. It is saved either
          way, and a picker that silently does nothing looks broken. */}
      {viewOverrides && (
        <p id={NOTE_ID} className="ff-muted">
          the {view.view.label} of the challenge board is painting the app{" "}
          {resolved.theme.label} right now. this choice takes over again on the{" "}
          {DEFAULT_VIEW.label}.
        </p>
      )}
    </>
  );
}
