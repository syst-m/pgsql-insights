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

/** R10.12's named states plus the detail route, as URLs. */
const SCREENS: readonly { name: string; url: string }[] = [
  { name: "fleet", url: "/?fixture=fleet" },
  { name: "empty", url: "/?fixture=empty" },
  { name: "partial", url: "/?fixture=partial" },
  { name: "unavailable", url: "/?fixture=unavailable" },
  // `?delay=` holds the fetch open so the loading panel is on screen when the
  // screenshot is taken; without it the fixture resolves in 60ms and the
  // capture races it.
  { name: "loading", url: "/?fixture=fleet&delay=60000" },
  { name: "detail", url: "/c/freeidp/freeidp-pg?fixture=fleet" },
  // R10.6's page, screenshotted now that routes.ts mounts a component there.
  // The page reads a QueryResponse and takes its own fixture when the URL names
  // none, so `?fixture=fleet` here is for the shell's benefit — the nav and the
  // theme — not the page's.
  { name: "queries", url: "/c/freeidp/freeidp-pg/queries?fixture=fleet" },
  // R10.3's page set is fourteen pages; the ones with components render, the
  // rest render the placeholder, and R16.7 screenshots one placeholder so the
  // shell itself is covered — the nav's links and the card that names the phase.
  // Which routes are planned is `routes.ts`'s answer, so this file names a URL
  // rather than restating a count (docs/agents.md §5 rule 10); the URL has to be
  // a route still without a component, which is why it moved off `/queries`
  // when that page got one. routes.test.tsx renders every planned route;
  // screenshotting near-identical cards would grow the matrix, not the coverage.
  { name: "planned", url: "/c/freeidp/freeidp-pg/databases?fixture=fleet" },
];

/** Font antialiasing differs a little between machines and OS versions; a
 *  small tolerance keeps the suite useful without letting a real regression
 *  (a missing column, a wrong colour) slip under it. */
const SHOT = { fullPage: true, maxDiffPixelRatio: 0.01 } as const;

for (const screen of SCREENS) {
  test(`renders the ${screen.name} state`, async ({ page }) => {
    await page.goto(screen.url);
    await expect(page.locator("main")).toBeVisible();
    await expect(page).toHaveScreenshot(`fleet-${screen.name}.png`, SHOT);
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
