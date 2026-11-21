/* The R10.13 theme control.
 *
 * The CSS declares dark values twice — once under
 * `@media (prefers-color-scheme: dark) { :root:where(:not([data-theme="light"])) }`
 * and once under `:root[data-theme="dark"]` — which is what lets an explicit
 * toggle win in both directions: choosing light inside a dark OS still gets
 * the light ramp, because the `:not([data-theme="light"])` guard disables the
 * media query. This component only writes the attribute; it never duplicates a
 * colour value.
 *
 * R10.13 also requires the charts to be *re-validated* per theme rather than
 * flipped: the dark series steps are their own steps from the same ramps, and
 * `scripts/validate_palette.js` was run against both surfaces (see
 * IMPLEMENTATION.md §4, D12's entry).
 */

import { useEffect, useState } from "react";

export type Theme = "light" | "dark" | "system";

export const THEME_STORAGE_KEY = "pgsi.theme";

/** Write the theme to an element. Exported for tests: it works on any
 *  Element, not just the document root. */
export function applyTheme(
  el: { setAttribute(name: string, value: string): void; removeAttribute(name: string): void },
  theme: Theme,
): void {
  if (theme === "system") el.removeAttribute("data-theme");
  else el.setAttribute("data-theme", theme);
}

/** Read a persisted theme, tolerating a storage that throws (private mode). */
export function readStoredTheme(storage: { getItem(k: string): string | null } | null): Theme {
  let raw: string | null = null;
  try {
    raw = storage?.getItem(THEME_STORAGE_KEY) ?? null;
  } catch {
    return "system";
  }
  return raw === "light" || raw === "dark" ? raw : "system";
}

export function storeTheme(storage: { setItem(k: string, v: string): void } | null, theme: Theme): void {
  try {
    storage?.setItem(THEME_STORAGE_KEY, theme);
  } catch {
    // Storage can throw in private browsing; the theme still applies for this
    // page load, which is the part that matters.
  }
}

/** The real `localStorage`, or null when it is absent or unusable.
 *
 * `typeof localStorage !== "undefined"` is not a sufficient check: Node ships
 * an experimental global `localStorage` that is present but method-less unless
 * `--localstorage-file` points somewhere valid (which is exactly the state a
 * Vitest jsdom run inherits), and Safari in private mode throws on access.
 * Both cases fall back to null — the theme still works for the page load, it
 * just does not persist. */
export function browserStorage(): {
  getItem(k: string): string | null;
  setItem(k: string, v: string): void;
} | null {
  try {
    const s = globalThis.localStorage as
      | { getItem(k: string): string | null; setItem(k: string, v: string): void }
      | undefined;
    if (typeof s?.getItem === "function" && typeof s?.setItem === "function") return s;
  } catch {
    // Accessing the property itself threw.
  }
  return null;
}

const OPTIONS: readonly { value: Theme; label: string }[] = [
  { value: "light", label: "Light" },
  { value: "dark", label: "Dark" },
  { value: "system", label: "System" },
];

export interface ThemeToggleProps {
  initial?: Theme;
  onChange?: (t: Theme) => void;
}

export function ThemeToggle({ initial, onChange }: ThemeToggleProps) {
  const [theme, setTheme] = useState<Theme>(() => initial ?? readStoredTheme(browserStorage()));

  useEffect(() => {
    if (typeof document === "undefined") return;
    applyTheme(document.documentElement, theme);
  }, [theme]);

  return (
    <div className="segmented" role="radiogroup" aria-label="Theme">
      {OPTIONS.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={theme === o.value}
          className={theme === o.value ? "active" : undefined}
          onClick={() => {
            setTheme(o.value);
            storeTheme(browserStorage(), o.value);
            onChange?.(o.value);
          }}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
