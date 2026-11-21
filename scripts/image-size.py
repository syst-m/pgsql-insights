#!/usr/bin/env python3
"""R11.4 image size report.

R11.4 asks for the *compressed* size of the image. The number that matters is the one
the registry reports: the sum of the layer sizes in the platform manifest. That is
different from `docker save | wc -c` (a tar of an already-unpacked rootfs, plus tar
framing) and from `docker images` (uncompressed on-disk size), which is why 0.1.0's
number was never trustworthy. This script reports the registry/OCI-layout number per
platform.

Targets (any mix):
  <dir>            an OCI layout directory (index.json + blobs/)  [-o type=oci,dir=…]
  <file.tar>       an OCI layout tarball                          [-o type=oci,dest=x.tar]
  <registry-ref>   host[:port]/repo[:tag], e.g.
                   10.10.111.134:5000/frankellis/pgsql-insights:dev
                   (a bare repo with no tag lists its tags instead of its sizes)

Usage:
  image-size.py [--max-mb N] [--json] TARGET [TARGET...]

Exit code is non-zero if any linux/amd64 target exceeds --max-mb (default 100, the
R11.4 CI failure threshold). Sizes above the < 60 MB *target* print WARN but do not
fail the build, so a regression is visible before it turns CI red.

R11.4's other number — the SPA's gzipped JS — is deliberately not here: it is a web
bundle measurement, not an image measurement, and CI reports it separately.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import tarfile
import urllib.request

INDEX_TYPES = (
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
)
MANIFEST_ACCEPT = ",".join(INDEX_TYPES + ("application/vnd.oci.image.manifest.v1+json",))


def fetch(url: str, accept: str | None = None) -> bytes:
    req = urllib.request.Request(url)
    if accept:
        req.add_header("Accept", accept)
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            return resp.read()
    except Exception as exc:  # noqa: BLE001 - one URL per call, so a failure is final
        raise SystemExit(f"image-size: cannot fetch {url}: {exc}")


class Target:
    """One image reference: an OCI layout dir, an OCI tarball, or a registry ref."""

    def __init__(self, target: str) -> None:
        self.target = target
        self.base = ""            # registry base URL, e.g. http://10.10.111.134:5000
        self.repo = ""            # repository, e.g. frankellis/pgsql-insights
        self.tag = ""             # tag, empty when the reference is a bare repo
        self.tar: tarfile.TarFile | None = None
        self.prefix = ""          # "" or "./" depending on how the tarball is laid out
        if os.path.isdir(target) and os.path.exists(os.path.join(target, "index.json")):
            self.kind = "dir"
        elif os.path.isfile(target):
            self.kind = "tar"
            self.tar = tarfile.open(target)
            self.prefix = "./" if "./index.json" in self.tar.getnames() else ""
        else:
            host, _, repo_tag = target.partition("/")
            if not repo_tag:
                raise SystemExit(f"image-size: bad registry reference {target!r}")
            self.kind = "registry"
            self.base = f"http://{host}" if ":" in host and not host.endswith(":443") else f"https://{host}"
            parts = repo_tag.split(":", 1)
            self.repo = parts[0]
            self.tag = parts[1] if len(parts) > 1 else ""

    def _read_layout(self, rel: str) -> bytes:
        """Read a file from an OCI layout directory or tarball."""
        if self.kind == "dir":
            with open(os.path.join(self.target, rel), "rb") as fh:
                return fh.read()
        assert self.tar is not None
        name = rel if rel in self.tar.getnames() else self.prefix + rel
        member = self.tar.extractfile(self.tar.getmember(name))
        assert member is not None
        return member.read()

    def blob(self, digest: str, media: str | None = None) -> bytes:
        """Raw bytes of a blob, addressed by its digest (e.g. sha256:abcd…)."""
        if self.kind == "registry":
            return fetch(f"{self.base}/v2/{self.repo}/blobs/{digest}", media)
        return self._read_layout("blobs/" + digest.replace(":", "/"))

    def document(self) -> dict:
        """The image index (or, for a bare repo, the tag list) at this reference."""
        if self.kind == "registry":
            if not self.tag:
                return json.loads(fetch(f"{self.base}/v2/{self.repo}/tags/list"))
            return json.loads(fetch(f"{self.base}/v2/{self.repo}/manifests/{self.tag}",
                                    MANIFEST_ACCEPT))
        return json.loads(self._read_layout("index.json").decode())

    def platforms(self) -> list[dict]:
        doc = self.document()
        if "manifests" not in doc:
            return [{"platform": "tags", "digest": None, "mediaType": None,
                     "tags": doc.get("tags", [])}]
        out = []
        for desc in doc["manifests"]:
            p = desc.get("platform") or {}
            out.append({"platform": f"{p.get('os', '?')}/{p.get('architecture', '?')}",
                        "digest": desc["digest"], "mediaType": desc.get("mediaType")})
        return out

    def compressed_size(self, digest: str, media: str | None) -> int:
        """Sum of the layer sizes in one manifest — the compressed byte count."""
        doc = json.loads(self.blob(digest, media).decode())
        return sum(int(layer["size"]) for layer in doc.get("layers", []))


def report(target: str) -> list[dict]:
    t = Target(target)
    rows: list[dict] = []
    for m in t.platforms():
        if m["platform"] == "tags":
            rows.append({"platform": "tags", "bytes": None, "mib": None,
                         "digest": None, "tags": m["tags"]})
            continue
        if m["mediaType"] in INDEX_TYPES:
            continue  # a nested index; only leaf manifests carry layers
        size = t.compressed_size(m["digest"], m["mediaType"])
        rows.append({"platform": m["platform"], "bytes": size,
                     "mib": round(size / 1048576, 3), "digest": m["digest"]})
    return rows


def main() -> int:
    ap = argparse.ArgumentParser(description="R11.4 compressed image size report")
    ap.add_argument("targets", nargs="+", help="OCI layout dir, OCI tarball, or registry ref")
    ap.add_argument("--max-mb", type=float, default=100.0, help="CI failure threshold (default 100)")
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()

    rows: dict[str, list[dict]] = {}
    failures: list[str] = []
    for target in args.targets:
        rows[target] = report(target)
        for r in rows[target]:
            if r["platform"] == "linux/amd64" and r["mib"] is not None and r["mib"] > args.max_mb:
                failures.append(f"{target}: linux/amd64 {r['mib']} MiB > {args.max_mb} MiB")

    if args.json:
        print(json.dumps(rows, indent=2))
    else:
        print(f"R11.4 compressed image size — CI fails above {args.max_mb} MiB, target < 60 MiB")
        for target, entries in rows.items():
            print(f"\n{target}")
            for r in entries:
                if r["platform"] == "tags":
                    print(f"  tags: {', '.join(r['tags'])}")
                    continue
                flag = "OK" if r["mib"] <= 60 else ("WARN" if r["mib"] <= args.max_mb else "FAIL")
                print(f"  {r['platform']:<16}{r['mib']:>9.3f} MiB {r['bytes']:>12} B  [{flag}]"
                      f"  {r['digest'][:23]}…")
        print("\nThe SPA's gzipped JS is a separate CI number (R11.4) and is not part of this report.")

    for failure in failures:
        print(f"error: {failure}", file=sys.stderr)
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
