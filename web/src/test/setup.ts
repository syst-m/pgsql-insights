/* Vitest setup (R16.7's component-suite half).
 *
 * jest-dom's matchers (`toBeVisible`, `toHaveAttribute`, `toBeChecked`) are what
 * let a component test assert on rendered state instead of on markup strings.
 * The matchers extend the `vitest` `expect`, so this file is the only place
 * they are registered — no per-file imports.
 */

import "@testing-library/jest-dom/vitest";

// jsdom does not implement `window.matchMedia`; the theme code and any
// responsive branch need it to exist even when it always answers "no".
if (typeof window !== "undefined" && typeof window.matchMedia !== "function") {
  window.matchMedia = (query: string): MediaQueryList =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }) as unknown as MediaQueryList;
}

// jsdom's `getComputedTextLength` is missing, which the chart's measure-first
// path probes for. Leaving it undefined makes the code take its documented
// fallback rather than throwing.
