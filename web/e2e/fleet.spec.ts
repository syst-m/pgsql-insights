/* R16.7 — the visual regression suite.
 *
 * One screenshot per R10.12 state that exists at this phase, each reached the
 * way the app itself reaches it: a URL. `?fixture=<name>` selects the named
 * payload (web/src/api/client.ts), so the suite never needs a live API and
 * every screenshot is reproducible — the fixture's `generatedAt` is a constant,
 * which is what stops the "Data as of" stamp from making every run differ.
 *
 * The interaction tests below the screenshots cover the half a picture cannot:
 * that a sort click rewrites the URL, that the back button undoes it, and that
 * the theme toggle writes the attribute the CSS keys off (R10.13).
 */

import { expect, test } from "@playwright/test";

// `import type` so this import is erased: a value import of `client.ts` would
// pull `fixtures.ts` into the spec's runtime, and `fixtures.ts`'s JSON imports
// carry no `with { type: "json" }` attribute, which Node's ESM loader (the
// runtime Playwright transpiles specs into) refuses. The type is what the
// SCREENS table needs; the payload itself is never called here.
import type { FixtureName } from "../src/api/client";
// The R16.8 contract file, imported rather than retyped — see the query-detail
// entry below. The attribute is what makes the same import work in Node and in
// Vite; `fixtures.ts:86` (`FIXTURE_QUERY_DETAIL`) is the app-side alias for it.
import apiQueryDetailJson from "../../testdata/api/query-detail.json" with { type: "json" };

/** One screenshot target: the name the baseline png is keyed on, and the URL
 *  that reaches it. */
interface Screen {
  readonly name: string;
  readonly url: string;
}

/** Build a screen URL so the `?fixture=` half is typed rather than typed out.
 *
 *  `fixture` is `FixtureName` (`web/src/api/client.ts:88` (`FixtureName`)) — the
 *  client's own union, the one `urlState.ts`'s `fixtureFlag` parses a URL into.
 *  Naming a fixture the API layer doesn't have used to be a string this file
 *  could get wrong in silence: the app falls back to a default payload, the
 *  screenshot matches the baseline it was cut from, and the test stays green
 *  while screenshotting a state it thinks it deleted. With the union imported —
 *  and `web/tsconfig.json` (`include`) covering this directory, which is what
 *  makes the import load-bearing rather than decorative, since Playwright
 *  transpiles specs with esbuild and typechecks nothing — a fixture name that
 *  is not in the contract is a `tsc` error.
 */
const screen = (name: string, path: string, fixture: FixtureName, extra = ""): Screen => ({
  name,
  url: `${path}?fixture=${fixture}${extra}`,
});

/** Every route `web/src/routes.ts` (`ROUTES`) has a component for, plus the
 *  R10.12 named states of the fleet route, plus one placeholder. Which routes
 *  have components is the route table's answer, so this list names routes and
 *  never restates a count (docs/agents.md §5 rule 10). */
const SCREENS: readonly Screen[] = [
  screen("fleet", "/", "fleet"),
  screen("empty", "/", "empty"),
  screen("partial", "/", "partial"),
  screen("unavailable", "/", "unavailable"),
  // `?delay=` holds the fetch open so the loading panel is on screen when the
  // screenshot is taken; without it the fixture resolves in 60ms and the
  // capture races it.
  screen("loading", "/", "fleet", "&delay=60000"),
  screen("detail", "/c/freeidp/freeidp-pg", "fleet"),
  // R10.6's page, screenshotted now that routes.ts mounts a component there.
  // The page reads a QueryResponse and takes its own fixture when the URL names
  // none, so `?fixture=fleet` here is for the shell's benefit — the nav and the
  // theme — not the page's.
  screen("queries", "/c/freeidp/freeidp-pg/queries", "fleet"),
  // R10.7's page. The fingerprint is the fixture's own, imported rather than
  // retyped: `web/src/api/client.ts:387` (`fetchQueryDetail`) answers every
  // fingerprint on the fixture arm with `FIXTURE_QUERY_DETAIL`, so a stale copy
  // of the string here would still render — the URL would name one query and the
  // page would show another, which is a screenshot of a state nobody asked for.
  // The cluster is the fixture's too (open-webui/open-webui-pg), because the
  // detail route's (namespace, cluster, fingerprint) triple is the key.
  screen(
    "query-detail",
    `/c/open-webui/open-webui-pg/queries/${apiQueryDetailJson.fingerprint}`,
    "fleet",
  ),
  // R10.8's active-queries page (Phase 6a) and R10.9's tag browser (Phase 5).
  // Both have components now, and both ignore their params on the fixture arm
  // (`web/src/api/client.ts:559` (`fetchActiveQueries`),
  // `web/src/api/client.ts:522` (`fetchTags`)), so the cluster here is the one
  // the rest of the suite uses rather than one the fixtures name.
  screen("active", "/c/freeidp/freeidp-pg/active", "fleet"),
  screen("tags", "/c/freeidp/freeidp-pg/tags", "fleet"),
  // R10.3's page set is fourteen pages; the ones with components render, the
  // rest render the placeholder, and R16.7 screenshots one placeholder so the
  // shell itself is covered — the nav's links and the card that names the phase.
  // Which routes are planned is `routes.ts`'s answer, so this file names a URL
  // rather than restating a count (docs/agents.md §5 rule 10); the URL has to be
  // a route still without a component, which is why it moved off `/queries`
  // when that page got one. routes.test.tsx renders every planned route;
  // screenshotting near-identical cards would grow the matrix, not the coverage.
  screen("planned", "/c/freeidp/freeidp-pg/databases", "fleet"),
];

/** Font antialiasing differs a little between machines and OS versions; a
 *  small tolerance keeps the suite useful without letting a real regression
 *  (a missing column, a wrong colour) slip under it. */
const SHOT = { fullPage: true, maxDiffPixelRatio: 0.01 } as const;

for (const entry of SCREENS) {
  test(`renders the ${entry.name} state`, async ({ page }) => {
    await page.goto(entry.url);
    await expect(page.locator("main")).toBeVisible();
    await expect(page).toHaveScreenshot(`fleet-${entry.name}.png`, SHOT);
  });
}

test("renders the fleet in dark mode (R10.13)", async ({ page }) => {
  // The media-query path, not the [data-theme] path: this is the theme a user
  // with a dark OS gets without touching the toggle, and it is the half of
  // R10.13 that a light-only screenshot cannot prove.
  await page.emulateMedia({ colorScheme: "dark" });
  await page.goto("/?fixture=fleet");
  await expect(page.locator("main")).toBeVisible();
  await expect(page).toHaveScreenshot("fleet-dark.png", SHOT);
});

test.describe("interactions", () => {
  test("a sort click rewrites the URL and reorders the rows", async ({ page }) => {
    await page.goto("/?fixture=fleet");
    // Default sort is health/descending, which puts the down cluster first.
    await expect(page.locator("table.data tbody tr").first()).toContainText("freeidp-pg");

    await page.getByRole("button", { name: "Cluster", exact: true }).click();
    await expect(page).toHaveURL(/\/\?sort=name&dir=asc&fixture=fleet/);
    // Alphabetical, not health-ordered: cookbook-pg now leads instead of the
    // down cluster freeidp-pg.
    await expect(page.locator("table.data tbody tr").first()).toContainText("cookbook-pg");
    // The fixture selection survives the navigation.
    await expect(page).toHaveURL(/fixture=fleet/);
  });

  test("the back button restores the previous view (R10.16)", async ({ page }) => {
    await page.goto("/?fixture=fleet");
    await page.getByRole("button", { name: "Cluster", exact: true }).click();
    await expect(page).toHaveURL(/sort=name/);
    await page.goBack();
    await expect(page).toHaveURL(/\/\?fixture=fleet$/);
    await expect(page.locator("th[aria-sort]")).toHaveText(/Health/);
  });

  test("the theme toggle writes the attribute the CSS keys off (R10.13)", async ({ page }) => {
    await page.goto("/?fixture=fleet");
    await expect(page.locator("html")).not.toHaveAttribute("data-theme");
    await page.getByRole("radio", { name: "Dark" }).click();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
    await page.getByRole("radio", { name: "System" }).click();
    await expect(page.locator("html")).not.toHaveAttribute("data-theme");
  });

  test("a row link opens that cluster's detail page", async ({ page }) => {
    await page.goto("/?fixture=fleet");
    await page.getByRole("link", { name: /cookbook-pg/ }).click();
    await expect(page).toHaveURL(/\/c\/cookbook\/cookbook-pg/);
    await expect(page.getByRole("heading", { level: 1, name: /cookbook-pg/ })).toBeVisible();
  });

  test("the nav reaches the R10.3 routes from a cluster page", async ({ page }) => {
    await page.goto("/c/freeidp/freeidp-pg?fixture=fleet");
    const nav = page.getByRole("navigation", { name: "Primary" });
    // The nav is the route table rendered, so the current page is marked…
    await expect(nav.getByRole("link", { name: "Cluster Overview", exact: true })).toHaveAttribute(
      "aria-current",
      "page",
    );
    // …and any other route is one click away, planned or not.
    await nav.getByRole("link", { name: "Wait Events", exact: true }).click();
    await expect(page).toHaveURL(/\/c\/freeidp\/freeidp-pg\/wait-events\?fixture=fleet$/);
    await expect(page.getByRole("heading", { level: 1, name: "Wait Events" })).toBeVisible();
    // Back to the fleet: the route changes, the cluster selection clears, and
    // the `?fixture=` flag still rides along so the data source doesn't switch.
    await nav.getByRole("link", { name: "Fleet", exact: true }).click();
    await expect(page).toHaveURL(/\/\?fixture=fleet$/);
  });

  test("the health chart's legend table matches the bar (R10.23's non-colour channel)", async ({ page }) => {
    await page.goto("/?fixture=fleet");
    const legend = page.locator("table.legend-table");
    await expect(legend.getByRole("row").nth(1)).toContainText("Healthy");
    await expect(legend.getByRole("row").nth(1)).toContainText("3");
    await expect(legend.getByRole("row").nth(3)).toContainText("Down");
    await expect(legend.getByRole("row").nth(3)).toContainText("1");
  });
});
