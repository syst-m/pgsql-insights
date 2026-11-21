/* R10.13's toggle. The CSS declares dark twice (media query + [data-theme]),
 * so the only job here is to write the attribute correctly. */

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { applyTheme, readStoredTheme, storeTheme, THEME_STORAGE_KEY, ThemeToggle } from "./ThemeToggle";

/** A click, wrapped in act() by the library, so React flushes the state update
 *  before the assertion runs (a raw dispatchEvent leaves it pending and the
 *  DOM still shows the old theme). */
function click(el: Element): void {
  fireEvent.click(el);
}

describe("applyTheme", () => {
  it("writes data-theme for the explicit themes", () => {
    const el = document.createElement("div");
    applyTheme(el, "dark");
    expect(el.getAttribute("data-theme")).toBe("dark");
    applyTheme(el, "light");
    expect(el.getAttribute("data-theme")).toBe("light");
  });

  it("removes the attribute for system, so the media query decides", () => {
    const el = document.createElement("div");
    applyTheme(el, "dark");
    applyTheme(el, "system");
    expect(el.hasAttribute("data-theme")).toBe(false);
  });
});

describe("storage", () => {
  it("defaults to system and rejects a stored value it does not know", () => {
    expect(readStoredTheme({ getItem: () => null })).toBe("system");
    expect(readStoredTheme({ getItem: () => "neon" })).toBe("system");
    expect(readStoredTheme({ getItem: () => "dark" })).toBe("dark");
    expect(readStoredTheme(null)).toBe("system");
  });

  it("writes under the app's own key", () => {
    const store = new Map<string, string>();
    const storage = {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
    };
    storeTheme(storage, "light");
    expect(store.get(THEME_STORAGE_KEY)).toBe("light");
  });
});

describe("ThemeToggle", () => {
  it("renders three options and marks the current one", () => {
    render(<ThemeToggle initial="dark" />);
    const buttons = screen.getAllByRole("radio");
    expect(buttons.map((b) => b.textContent)).toEqual(["Light", "Dark", "System"]);
    expect(buttons[1]).toHaveAttribute("aria-checked", "true");
    expect(buttons[0]).toHaveAttribute("aria-checked", "false");
  });

  it("applies the chosen theme to the document root", () => {
    render(<ThemeToggle initial="system" />);
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    click(screen.getByRole("radio", { name: "Dark" }));
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
  });
});
