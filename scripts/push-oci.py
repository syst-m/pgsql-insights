#!/usr/bin/env python3
"""Push one or more OCI-layout directories into a plain docker/distribution registry.

Phase 0's publish path that does not need `docker login` (IMPLEMENTATION.md §5): the
dev machine's docker config has no usable ghcr.io credentials, so the multi-arch image
is published with the registry v2 HTTP API instead — the same API `docker push` speaks.
It is how the reference environment's image at 10.10.111.134:5000/frankellis/pgsql-insights
(R11.14's deploy target) was produced, and it works against any registry:2 / ghcr.io.

Usage:
  push-oci.py [--registry HOST[:PORT]] [--repo REPO] [--tag TAG] LAYOUT [LAYOUT...]

A LAYOUT is a directory containing index.json + blobs/ — exactly what
`docker buildx build --output type=oci,dir=…` writes. Pass one layout per platform to
publish a multi-arch image (R11.2), or a single layout for a single-platform push.
"""
import argparse
import hashlib
import json
import os
import sys
import urllib.error
import urllib.request

# Defaults are the reference environment's registry (docs/research/live-probe.md);
# override with the flags below or the REGISTRY/REPO/TAG environment variables.
REG = "10.10.111.134:5000"
REPO = "frankellis/pgsql-insights"
TAG = "dev"
SCHEMA = "http"


def request(method, path, data=None, ctype=None):
    url = path if path.startswith("http") else f"{SCHEMA}://{REG}{path}"
    r = urllib.request.Request(url, data=data, method=method)
    if ctype:
        r.add_header("Content-Type", ctype)
    try:
        with urllib.request.urlopen(r, timeout=180) as resp:
            return resp.status, dict(resp.getheaders()), resp.read()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read()


def upload_blob(blob_path):
    with open(blob_path, "rb") as fh:
        data = fh.read()
    digest = "sha256:" + hashlib.sha256(data).hexdigest()
    st, _, _ = request("HEAD", f"/v2/{REPO}/blobs/{digest}")
    if st == 200:
        return digest, "exists"
    st, headers, body = request("POST", f"/v2/{REPO}/blobs/uploads/")
    if st not in (201, 202):
        raise RuntimeError(f"POST uploads for {digest}: {st} {body[:200]!r}")
    loc = headers.get("Location") or headers.get("location")
    if not loc:
        raise RuntimeError(f"no Location for {digest}: {headers}")
    url = loc if loc.startswith("http") else f"{SCHEMA}://{REG}{loc}"
    url += ("&" if "?" in url else "?") + f"digest={digest}"
    st, _, body = request("PUT", url, data, "application/octet-stream")
    if st != 201:
        raise RuntimeError(f"PUT blob {digest}: {st} {body[:200]!r}")
    return digest, "uploaded"


def put_manifest(ref, payload, ctype):
    st, _, body = request("PUT", f"/v2/{REPO}/manifests/{ref}", payload, ctype)
    if st not in (200, 201):
        raise RuntimeError(f"PUT manifest {ref}: {st} {body[:300]!r}")
    return st


def push_layout(layout_dir):
    """Upload every blob + every image manifest in an OCI layout dir.

    Returns the list of (mediaType, digest, size, platform) descriptors.
    """
    with open(os.path.join(layout_dir, "index.json"), "rb") as fh:
        idx = json.load(fh)
    for name in sorted(os.listdir(os.path.join(layout_dir, "blobs", "sha256"))):
        upload_blob(os.path.join(layout_dir, "blobs", "sha256", name))
    descs = []
    for m in idx["manifests"]:
        mt = m["mediaType"]
        digest = m["digest"]
        with open(os.path.join(layout_dir, "blobs", "sha256", digest.split(":")[1]), "rb") as fh:
            payload = fh.read()
        if mt.endswith("image.manifest.v1+json"):
            put_manifest(digest, payload, mt)
            descs.append((mt, digest, m["size"], m.get("platform")))
        elif mt.endswith("image.index.v1+json"):
            # Nested index (multi-platform export): flatten its children.
            sub = json.loads(payload)
            for c in sub["manifests"]:
                put_manifest(c["digest"],
                             open(os.path.join(layout_dir, "blobs", "sha256",
                                               c["digest"].split(":")[1]), "rb").read(),
                             c["mediaType"])
                descs.append((c["mediaType"], c["digest"], c["size"], c.get("platform")))
    return descs


def main():
    global REG, REPO, TAG, SCHEMA
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--registry", default=os.environ.get("REGISTRY", REG),
                    help="registry host[:port], e.g. 10.10.111.134:5000 or ghcr.io")
    ap.add_argument("--repo", default=os.environ.get("REPO", REPO),
                    help="repository, e.g. frankellis/pgsql-insights")
    ap.add_argument("--tag", default=os.environ.get("TAG", "dev"))
    ap.add_argument("--scheme", default=os.environ.get("SCHEME", ""),
                    help="http|https; auto-detected from the port when empty")
    ap.add_argument("layouts", nargs="+", help="OCI layout dirs, one per platform")
    args = ap.parse_args()

    REG, REPO, TAG = args.registry, args.repo, args.tag
    SCHEMA = args.scheme or ("http" if ":" in REG and not REG.endswith(":443") else "https")

    descs = []
    for layout in args.layouts:
        descs.extend(push_layout(layout))
    if not descs:
        raise SystemExit("no manifests found")
    index = {
        "schemaVersion": 2,
        "mediaType": "application/vnd.oci.image.index.v1+json",
        "manifests": [
            {"mediaType": mt, "digest": d, "size": s, "platform": p}
            for mt, d, s, p in descs if p
        ],
    }
    payload = json.dumps(index, separators=(",", ":")).encode()
    digest = "sha256:" + hashlib.sha256(payload).hexdigest()
    put_manifest(digest, payload, "application/vnd.oci.image.index.v1+json")
    put_manifest(TAG, payload, "application/vnd.oci.image.index.v1+json")
    print(json.dumps({"image": f"{REG}/{REPO}:{TAG}", "scheme": SCHEMA,
                      "index_digest": digest,
                      "manifests": [d for _, d, _, _ in descs]}, indent=2))


if __name__ == "__main__":
    main()
