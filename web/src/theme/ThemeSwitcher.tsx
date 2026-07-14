import { useTheme } from "./provider";

// The picker: built-in themes plus a "system" option that follows the OS. Choosing
// one persists it and applies instantly; there is no save button and no reload.
export function ThemeSwitcher() {
  const { themes, preference, setPreference, system, resolved } = useTheme();
  const value = preference ?? system;

  return (
    <label className="theme-switcher field">
      theme
      <select value={value} onChange={(e) => setPreference(e.target.value)}>
        <option value={system}>system ({resolved.theme.label})</option>
        {themes.map((t) => (
          <option key={t.name} value={t.name}>
            {t.label}
          </option>
        ))}
      </select>
    </label>
  );
}
