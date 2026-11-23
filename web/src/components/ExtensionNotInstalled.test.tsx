/* R10.27's screen, tested for the three things the requirement makes testable.
 *
 * R10.27 says "designed and tested", and the testable readings of that are:
 *
 * 1. **It is a screen, not a string.** The R10.12 naming survives
 *    (`state-panel` + `data-state="unavailable"`), so R16.7's visual suite still
 *    finds it by state name, and it is more than the copy table's three fields.
 * 2. **The flow is the spec's.** R3.15 names four steps in an order, and the one
 *    this screen *is* (the offer) has to be marked as such — otherwise the screen
 *    describes a flow the viewer cannot locate themselves in.
 * 3. **The cost is on screen.** R3.15's "the restart cost is stated in the offer,
 *    not discovered afterwards" is a requirement about a UI element, so it is
 *    assertable: a reader of this screen learns about the rolling restart here,
 *    not after clicking.
 *
 * Plus the two rules that apply to any component here: R12.8's read-only case
 * (no `--allow-actions` ⇒ no button, and the CLI line is the whole offer) and
 * R10.23's untrusted-label rule (the cluster id comes from the URL bar and must
 * be text, never markup).
 */

import { fireEvent, render, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { ExtensionNotInstalled } from "./ExtensionNotInstalled";

describe("ExtensionNotInstalled (R10.27)", () => {
  it("keeps R10.12's state naming while being a screen instead of a string", () => {
    const { container } = render(<ExtensionNotInstalled cluster="ns-a/pg-1" />);
    const panel = container.querySelector(".state-panel") as HTMLElement;
    expect(panel.getAttribute("data-state")).toBe("unavailable");
    expect(panel.getAttribute("role")).toBe("status");
    // The generic panel's whole rendering is title + body + button; the screen
    // has sections, so a test that only checked the heading could not tell the
    // two apart.
    expect(within(panel).getAllByText(/What enabling it takes|The cost, stated here/)).toHaveLength(2);
  });

  it("names the cluster the URL addressed", () => {
    const { container } = render(<ExtensionNotInstalled cluster="open-webui/open-webui-pg" />);
    expect(container.textContent).toContain("open-webui/open-webui-pg");
  });

  it("renders both spellings of an absent cluster as the shared null value", () => {
    // `UrlState.cluster` is `string | null`: null on the fleet route, and "" for
    // a URL with an empty `?c=`. A screen rendered for a cluster the URL never
    // named says so with the token every other empty field uses — `NULL_VALUE` —
    // rather than printing "Cluster " with nothing after it.
    const nullCase = render(<ExtensionNotInstalled cluster={null} />);
    expect(nullCase.container.querySelector(".ext-subtitle")?.textContent).toContain("—");
    const emptyCase = render(<ExtensionNotInstalled cluster="" />);
    expect(emptyCase.container.querySelector(".ext-subtitle")?.textContent).toContain("—");
  });

  it("renders R3.15's four steps in R3.15's order, marking exactly one as here", () => {
    const { container } = render(<ExtensionNotInstalled cluster="ns-a/pg-1" />);
    const steps = Array.from(container.querySelectorAll(".ext-step"));
    expect(steps.map((s) => s.getAttribute("data-step"))).toEqual(["detect", "offer", "patch", "verify"]);
    // The screen is the *offer* step: detect is done (this screen is its output),
    // patch and verify are pending. Two of those would be a flow the viewer
    // cannot read.
    const here = steps.filter((s) => s.getAttribute("data-step-state") === "here");
    expect(here).toHaveLength(1);
    expect(here[0]?.getAttribute("data-step")).toBe("offer");
  });

  it("puts the step states in words, not only in styling", () => {
    // R10.23's "the colour never carries meaning alone", applied to a non-chart
    // surface: the marker is text content, so it survives a theme switch and a
    // reader who cannot see the tint.
    const { container } = render(<ExtensionNotInstalled cluster="ns-a/pg-1" />);
    const states = Array.from(container.querySelectorAll(".ext-step-state")).map((n) => n.textContent);
    expect(states).toEqual(["done", "here", "pending", "pending"]);
  });

  it("states the restart cost on the screen, as R3.15 requires", () => {
    // "The restart cost is stated in the offer, not discovered afterwards." The
    // only way to test that sentence is to read the offer before clicking it.
    const { container } = render(<ExtensionNotInstalled cluster="ns-a/pg-1" />);
    const cost = container.querySelector(".ext-cost");
    expect(cost).not.toBeNull();
    expect(cost?.textContent).toContain("Rolling restart");
    expect(cost?.textContent).toContain("Reversible");
    expect(cost?.textContent).toContain("One cluster per invocation");
  });

  it("renders the offer as a button and fires it", () => {
    const onEnable = vi.fn();
    const { getByRole } = render(<ExtensionNotInstalled cluster="ns-a/pg-1" onEnable={onEnable} />);
    const button = getByRole("button", { name: "Enable pg_stat_statements" });
    fireEvent.click(button);
    expect(onEnable).toHaveBeenCalledTimes(1);
  });

  it("drops the button when the host has no action, and keeps the CLI offer", () => {
    // R12.8's gate is off by default, so a read-only deployment has this screen
    // and no mutation to offer. The button disappears; the CLI line does not,
    // because with no button it is the whole offer.
    const { container, queryByRole } = render(<ExtensionNotInstalled cluster="ns-a/pg-1" />);
    expect(queryByRole("button")).toBeNull();
    expect(container.querySelector(".ext-cli")?.textContent).toContain(
      "pgsi enable pg-stat-statements --cluster ns-a/pg-1",
    );
    expect(container.querySelector(".ext-cli")?.textContent).toContain("--allow-actions");
  });

  it("renders the cluster id as text, never as markup", () => {
    // R10.23: "labels are untrusted data → textContent never innerHTML". The
    // cluster id is a URL segment, so it is exactly the input that rule exists
    // for; a `<img>` in it must come back as characters, not as a node.
    const { container } = render(<ExtensionNotInstalled cluster='<img src="x">' />);
    expect(container.querySelector(".ext-head img")).toBeNull();
    expect(container.querySelector(".ext-subtitle")?.textContent).toBe('Cluster <img src="x">');
  });
});
