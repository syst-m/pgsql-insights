/* R10.21's timestamp contract plus the shared number formatters. */

import { describe, expect, it } from "vitest";

import {
  NULL_VALUE,
  daysUntil,
  formatDurationMs,
  formatNumber,
  formatPercent,
  formatSize,
  formatTimestamp,
  localZone,
  parseDate,
  utcTitle,
} from "./format";

const SAMPLE = "2026-11-20T11:04:12Z";

describe("formatTimestamp", () => {
  it("renders the date and clock in the local zone and labels the zone", () => {
    const zone = localZone();
    expect(zone).not.toBe("");
    const out = formatTimestamp(SAMPLE);
    // The zone label is part of the string, not only a tooltip (R10.21).
    const expectedZone = new Intl.DateTimeFormat("en-GB", { timeZone: zone, timeZoneName: "short" })
      .formatToParts(new Date(SAMPLE))
      .find((p) => p.type === "timeZoneName")?.value;
    expect(out).toContain(expectedZone ?? "");
  });

  it("renders the same instant as the platform's own local formatting", () => {
    const zone = localZone();
    const parts = new Intl.DateTimeFormat("en-GB", {
      timeZone: zone,
      day: "2-digit",
      month: "short",
      year: "numeric",
    }).formatToParts(new Date(SAMPLE));
    const day = parts.find((p) => p.type === "day")?.value;
    const year = parts.find((p) => p.type === "year")?.value;
    expect(formatTimestamp(SAMPLE)).toContain(`${day} ${parts.find((p) => p.type === "month")?.value} ${year}`);
  });

  it("can drop the clock and the zone for a compact cell", () => {
    const out = formatTimestamp(SAMPLE, { withTime: false, withZone: false });
    expect(out).not.toContain(":");
  });

  it("renders null and garbage as an em dash, never 'Invalid Date'", () => {
    expect(formatTimestamp(null)).toBe(NULL_VALUE);
    expect(formatTimestamp("")).toBe(NULL_VALUE);
    expect(formatTimestamp("not a date")).toBe(NULL_VALUE);
  });
});

describe("utcTitle", () => {
  it("gives the absolute UTC value for the hover (R10.21)", () => {
    expect(utcTitle(SAMPLE)).toBe(`${SAMPLE} UTC`);
    expect(utcTitle(null)).toBe("no timestamp");
  });
});

describe("parseDate", () => {
  it("accepts RFC 3339 and rejects the rest", () => {
    // Compare the instant, not the string: JS normalises "…12Z" to
    // "…12.000Z", so toISOString() is not the payload's own text.
    expect(parseDate(SAMPLE)?.getTime()).toBe(Date.parse(SAMPLE));
    expect(parseDate("11/20/2026")).toBeNull();
    expect(parseDate("0000-00-00")).toBeNull();
  });
});

describe("formatNumber", () => {
  it("groups thousands and keeps the requested digits", () => {
    expect(formatNumber(1234567)).toBe("1,234,567");
    expect(formatNumber(12.345, 2)).toBe("12.35");
    expect(formatNumber(0)).toBe("0");
  });

  it("renders null as the em dash, not as zero", () => {
    expect(formatNumber(null)).toBe(NULL_VALUE);
    expect(formatNumber(Number.NaN)).toBe(NULL_VALUE);
  });
});

describe("formatPercent", () => {
  it("appends the unit and keeps one decimal", () => {
    expect(formatPercent(12.34)).toBe("12.3%");
    expect(formatPercent(0)).toBe("0.0%");
    expect(formatPercent(null)).toBe(NULL_VALUE);
  });
});

describe("formatDurationMs", () => {
  it("switches units where the extra digits stop meaning anything", () => {
    expect(formatDurationMs(842)).toBe("842 ms");
    expect(formatDurationMs(1420)).toBe("1.42 s");
    expect(formatDurationMs(64_000)).toBe("1m 04s");
    expect(formatDurationMs(null)).toBe(NULL_VALUE);
  });
});

describe("formatSize", () => {
  it("passes Kubernetes quantities through verbatim", () => {
    expect(formatSize("10Gi")).toBe("10Gi");
    expect(formatSize("2Gi")).toBe("2Gi");
  });

  it("formats raw byte counts", () => {
    expect(formatSize(2048)).toBe("2.0 KiB");
    expect(formatSize(1_500_000_000)).toBe("1.4 GiB");
    expect(formatSize(null)).toBe(NULL_VALUE);
  });
});

describe("daysUntil", () => {
  it("counts days and goes negative once expired", () => {
    const now = new Date("2026-11-20T00:00:00Z");
    expect(daysUntil("2026-12-11T06:52:19Z", now)).toBe(21);
    expect(daysUntil("2026-11-10T00:00:00Z", now)).toBe(-10);
    expect(daysUntil(null, now)).toBeNull();
  });
});
