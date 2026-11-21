/* R16.7's visual-regression harness.
 *
 * The suite runs against the *built* bundle served by `vite preview`, not
 * against `vite dev`: dev serves the source with HMR transforms, and the thing
 * shipping is the file `go:embed` puts inside the binary. `make test-web`
 * builds first, so what Playwright screenshots is byte-identical to what
 * `pgsi serve` serves.
 *
 * One project (chromium) is deliberate. The app is CSS-only responsive; adding
 * firefox/webkit triples the screenshot matrix and the flake rate for no extra
 * coverage of the requirements, and CI on a linux runner renders fonts
 * differently from macOS anyway — hence the generous maxDiffPixelRatio.
 */

import { defineConfig, devices } from "@playwright/test";

const PORT = 4173;
const BASE_URL = `http://127.0.0.1:${PORT}`;

export default defineConfig({
  testDir: "./e2e",
  // Screenshots are committed: a diff in review *is* the visual review.
  snapshotDir: "./e2e/__snapshots__",
  snapshotPathTemplate: "{snapshotDir}/{testFileDir}/{arg}{ext}",
  outputDir: "./e2e/.artifacts",
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  reporter: [
    ["list"],
    ["html", { outputFolder: "./e2e/report", open: "never" }],
    ["junit", { outputFile: "./e2e/report/junit.xml" }],
  ],
  use: {
    baseURL: BASE_URL,
    viewport: { width: 1440, height: 900 },
    // Fixed scale factor: a Retina capture and a CI capture must produce the
    // same pixels or every diff is noise.
    deviceScaleFactor: 1,
    reducedMotion: "reduce",
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  webServer: {
    // `--host 127.0.0.1` is not cosmetic: vite preview defaults to binding
    // `localhost`, which on macOS resolves to ::1 first, and Playwright's
    // readiness probe against 127.0.0.1 then times out.
    command: `npm run preview -- --host 127.0.0.1 --port ${PORT} --strictPort`,
    url: BASE_URL,
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
