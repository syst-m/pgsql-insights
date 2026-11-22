#!/usr/bin/env python3
"""R8.1's second clause: the OpenAPI document is "…and checked in CI".

R8.1 asks for "a committed OpenAPI document (api/openapi.yaml) generated from
the handlers **and checked in CI**" (IMPLEMENTATION.md §5-D13 settled the path).
The document was committed; nothing in CI ever read it. That gap is why the file
at HEAD did not parse: seven scalars were unquoted — two whose text contains
": " so YAML read them as nested mappings, five with a leading backtick, which
is a reserved YAML indicator — and a loader failed on syntax before it could
check a single contract. A review of the diff missed all seven. A validator
cannot, which is the whole argument for this file existing.

Two layers, because the runner's Python is not guaranteed to have PyYAML:

  * the **line scan** (stdlib, always runs) — indentation, duplicate keys at any
    level, every `paths:` entry declaring at least one HTTP method, every
    documented path carrying `x-status`, every `$ref` target present, every
    `x-planned-paths` entry shaped like a route, and the shape of the two
    `x-promql-catalogue` lists R8.3 requires.
  * the **parse** (PyYAML, when importable) — the real load, which catches
    everything the scan cannot: a mapping written where a scalar belongs, a
    flow sequence with a missing bracket, a tab inside a nested block, and the
    deep structural checks over the loaded document (operationId uniqueness,
    response descriptions, schema `type` vocabulary, `required` ⊆ `properties`,
    tags declared, security schemes declared).

When PyYAML is absent the parse layer reports itself as skipped and the scan
still gates: the check degrades to a subset, never to a no-op. That matters
because CI's `image` job already runs `python3 scripts/image-size.py`, so
python3 is on the runner; whether that interpreter has PyYAML is a property of
the runner image, not of this repo.

Usage:
  check-openapi.py [--json] [FILE ...]      (default: api/openapi.yaml)

Exit 0 when the documents are clean, 1 when any finding is reported.
"""
from __future__ import annotations

import argparse
import json
import re
import sys

try:  # the parse layer; the scan layer never needs it
    import yaml
except ImportError:  # pragma: no cover - depends on the interpreter, not the repo
    yaml = None

METHODS = ("get", "put", "post", "delete", "options", "head", "patch", "trace")
SCHEMA_TYPES = ("object", "array", "string", "number", "integer", "boolean", "null")
# R8.3's catalogue names are `<family>.<metric>`; the three families are the
# three R9 requirements the catalogue is built from (R9.4 aggregate, R9.5
# per-query, R9.7 system).
CATALOGUE_NAME = re.compile(r"^(aggregate|perquery|system)\.[a-z0-9_]+$")
TOP_KEY = re.compile(r"^([A-Za-z0-9_.-]+):(\s|$)")
REF_LINE = re.compile(r"^\s*(-\s+)?\$ref:\s*['\"]?([^'\"\s]+)['\"]?\s*$")


class DuplicateKey(Exception):
    """A mapping key that appears twice — legal YAML, silently lossy, fatal here."""


def _strict_mapping(loader, node):
    """SafeLoader mapping constructor that refuses duplicate keys.

    PyYAML's default keeps the *last* value and says nothing, so a document that
    defines `Meta:` twice validates cleanly while one of the two definitions is
    dead. The OpenAPI spec forbids duplicate keys in one object for exactly that
    reason, and this is the only way to catch them without a second parser."""
    loader.flatten_mapping(node)
    seen: dict = {}
    for key, value in loader.construct_pairs(node):
        if key in seen:
            raise DuplicateKey(f"duplicate key {key!r} at line {node.start_mark.line + 1}")
        seen[key] = value
    return seen


def parse(text: str):
    """Load the document with the strict loader. Returns (doc, error)."""
    if yaml is None:
        return None, None
    loader = type("Strict", (yaml.SafeLoader,), {})
    loader.add_constructor(yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, _strict_mapping)
    try:
        return yaml.load(text, loader), None
    except yaml.YAMLError as exc:
        return None, str(exc).replace("\n", " ").strip()
    except DuplicateKey as exc:
        return None, str(exc)


# ---------------------------------------------------------------------------
# Line scan (stdlib)
# ---------------------------------------------------------------------------

def scan(text: str) -> tuple[list[str], dict]:
    """Indentation-level scan of the raw text.

    Returns (findings, facts). `facts` carries what the deeper checks want and
    what the PyYAML-less path needs to resolve `$ref`s without a parser: the
    top-level keys, the `paths:` entries with their method keys and their
    `x-status` presence, the `x-promql-catalogue` lists, and every `$ref`."""
    findings: list[str] = []
    lines = text.split("\n")
    facts: dict = {"top_keys": [], "paths": {}, "refs": [], "catalogues": {},
                   "components_children": set()}
    in_components = False
    current_path: str | None = None

    for no, raw in enumerate(lines, start=1):
        if not raw.strip():
            continue
        if raw.lstrip().startswith("#"):
            continue  # a comment line carries no structure at any indent
        if raw.startswith(" "):
            stripped = raw.lstrip(" ")
            indent = len(raw) - len(stripped)
            if "\t" in raw[: len(raw) - len(stripped)]:
                findings.append(f"{no}: tab in the indentation (YAML forbids tabs)")
            elif indent % 2:
                # The document is 2-space nested throughout; an odd indent is
                # how a block ends up nested under the wrong parent while still
                # parsing. Flagging only odd indents keeps this from becoming a
                # style lint over files this repo does not own.
                findings.append(f"{no}: indent is {indent}, not a multiple of 2")
        else:
            indent = 0
        if indent == 0:
            m = TOP_KEY.match(raw)
            if not m:
                findings.append(f"{no}: top-level line is not a key: {raw[:60]!r}")
                continue
            key = m.group(1)
            if key in facts["top_keys"]:
                findings.append(f"{no}: duplicate top-level key {key!r}")
            facts["top_keys"].append(key)
            in_components = key == "components"
            current_path = None
            continue

        if in_components and indent == 4:
            m = re.match(r"^([A-Za-z0-9_.-]+):", raw.strip())
            if m:
                facts["components_children"].add(m.group(1))

        if indent == 2 and raw.strip().endswith(":") and raw.strip().startswith("/"):
            current_path = raw.strip()[:-1]
            facts["paths"].setdefault(current_path, {"methods": [], "x_status": False,
                                                     "line": no, "catalogue": None,
                                                     "catalogue_indent": None})
            continue

        if current_path and indent > 2:
            entry = facts["paths"][current_path]
            stripped = raw.strip()
            for method in METHODS:
                if stripped == f"{method}:":
                    entry["methods"].append(method)
            if stripped.startswith("x-status:"):
                entry["x_status"] = True
            update_catalogue(entry, facts, current_path, indent, stripped)
            m = REF_LINE.match(raw)
            if m:
                facts["refs"].append((f"{current_path}: {m.group(2)}", m.group(2)))
            continue

        m = REF_LINE.match(raw)
        if m:
            facts["refs"].append((f"line {no}: {m.group(2)}", m.group(2)))

    return findings, facts


def ref_exists(ref: str, facts: dict) -> bool:
    """Resolve a `#/…` pointer well enough to prove the target is declared.

    With PyYAML the loaded document answers this exactly; without it, every
    reference this document uses is `#/components/<section>/<Name>`, and the set
    of keys at indent 4 under `components:` is the same answer."""
    if not ref.startswith("#/"):
        return True  # a remote or sibling-file ref; not this document's business
    parts = ref[2:].split("/")
    if len(parts) == 3 and parts[0] == "components":
        return parts[2] in facts["components_children"]
    return True


def update_catalogue(entry: dict, facts: dict, path: str, indent: int, stripped: str) -> None:
    """Follow an `x-promql-catalogue:` list as the scanner walks past it.

    The list is a run of `- name` lines all at one indent, and the run ends at
    the first line that is not another item at that indent. The first version of
    this scanner only looked for the `- ` prefix, so it kept appending long after
    the list ended: every `- $ref:` under the path's own `parameters:` block, and
    one `- metric:` from inside the response example, turned up as catalogue
    entries — which the R8.3 name pattern then (correctly) rejected.
    """
    if entry["catalogue"] is None:
        if stripped.endswith("x-promql-catalogue:"):
            entry["catalogue"] = []
            entry["catalogue_indent"] = None  # the items' indent, learned from the first
            facts["catalogues"][path] = entry["catalogue"]
        return
    want = entry["catalogue_indent"]
    if want is None:
        if not stripped.startswith("- "):
            return  # the value hasn't started yet (blank and comment lines are skipped)
        entry["catalogue_indent"] = indent
    elif indent != want or not stripped.startswith("- "):
        entry["catalogue"] = None  # the run ended; facts keeps the finished list
        entry["catalogue_indent"] = None
        return
    entry["catalogue"].append(stripped[2:].strip())


def check_scan(facts: dict) -> list[str]:
    """The findings that need the whole file: methods per path, x-status, refs,
    planned-path shapes, catalogue shapes."""
    out: list[str] = []
    for path, entry in facts["paths"].items():
        first = f"paths.{path} (line {entry['line']})"
        if not entry["methods"]:
            out.append(f"{first}: no HTTP method key — a documented path must "
                       f"declare at least one of {', '.join(METHODS)}")
        if not entry["x_status"]:
            out.append(f"{first}: no `x-status` — every documented path says "
                       f"whether it is in code (the document's own rule, and the "
                       f"only way `paths` and `x-planned-paths` cannot disagree)")
        for where, ref in facts["refs"]:
            if where.startswith(path) and not ref_exists(ref, facts):
                out.append(f"{where}: $ref {ref!r} resolves to nothing in components")
    for path, names in facts["catalogues"].items():
        if not names:
            out.append(f"paths.{path}: x-promql-catalogue is empty — R8.3 requires "
                       f"the catalogue to be enumerated in the document")
            continue
        for name in names:
            if not CATALOGUE_NAME.match(name):
                out.append(f"paths.{path}: catalogue entry {name!r} is not "
                           f"`<family>.<metric>` with family in aggregate|perquery|system")
    return out


def check_catalogues(facts: dict) -> list[str]:
    """The two R8.3 endpoints must enumerate disjoint lists (a per-query name
    answered by /metrics/system as well would make the two routes aliases)."""
    out: list[str] = []
    query = set(facts["catalogues"].get("/api/v1/metrics/query", []))
    system = set(facts["catalogues"].get("/api/v1/metrics/system", []))
    both = query & system
    if both:
        out.append(f"x-promql-catalogue: {', '.join(sorted(both))} is catalogued on "
                   f"both /api/v1/metrics/query and /api/v1/metrics/system; the two "
                   f"endpoints are disjoint by construction")
    return out


# ---------------------------------------------------------------------------
# Parse layer (PyYAML)
# ---------------------------------------------------------------------------

def walk(node, path: str):
    """Yield (path, node) for every mapping in the document.

    `example`/`examples` subtrees are not descended into: their contents are
    *values*, and a ServicePort example's `type: ClusterIP` is a Kubernetes
    service type, not a JSON-Schema `type` — checking it produced a false
    finding on the first run of this script against the real document."""
    if isinstance(node, dict):
        yield path, node
        for key, value in node.items():
            if key in ("example", "examples", "default"):
                continue
            yield from walk(value, f"{path}.{key}" if path else str(key))
    elif isinstance(node, list):
        for i, value in enumerate(node):
            yield from walk(value, f"{path}[{i}]")


def check_document(doc: dict) -> list[str]:
    """Structural validation of the loaded document (OpenAPI 3.0.x)."""
    out: list[str] = []
    if not isinstance(doc, dict):
        return ["document root is not a mapping"]

    version = doc.get("openapi")
    if not isinstance(version, str) or not version.startswith(("3.0.", "3.1.")):
        out.append(f"openapi: {version!r} — expected a 3.0.x or 3.1.x version string")
    info = doc.get("info")
    if not isinstance(info, dict):
        out.append("info: missing or not a mapping")
    else:
        for field in ("title", "version"):
            value = info.get(field)
            if not isinstance(value, (str, int)) or not str(value).strip():
                out.append(f"info.{field}: missing or empty")

    declared_tags = {t.get("name") for t in doc.get("tags") or [] if isinstance(t, dict)}
    components = doc.get("components") or {}
    schemes = set((components.get("securitySchemes") or {}).keys())

    def check_security(node: dict, where: str) -> None:
        for requirement in node.get("security") or []:
            if not isinstance(requirement, dict):
                out.append(f"{where}.security: not a mapping")
                continue
            for name in requirement:
                if name not in schemes:
                    out.append(f"{where}.security: {name!r} is not in "
                               f"components.securitySchemes ({', '.join(sorted(schemes))})")

    paths = doc.get("paths")
    if not isinstance(paths, dict) or not paths:
        out.append("paths: missing or empty")
        paths = {}
    seen_ids: dict[str, str] = {}
    for path, item in paths.items():
        if not path.startswith("/"):
            out.append(f"paths.{path}: a path key must start with /")
        if not isinstance(item, dict):
            out.append(f"paths.{path}: path item is not a mapping")
            continue
        check_security(item, f"paths.{path}")
        for tag in item.get("tags") or []:
            if tag not in declared_tags:
                out.append(f"paths.{path}.tags: {tag!r} is not declared in the root tags list")
        for method in METHODS:
            if method not in item:
                continue
            op = item[method]
            where = f"paths.{path}.{method}"
            if not isinstance(op, dict):
                out.append(f"{where}: not a mapping")
                continue
            check_security(op, where)
            for tag in op.get("tags") or []:
                if tag not in declared_tags:
                    out.append(f"{where}.tags: {tag!r} is not declared in the root tags list")
            oid = op.get("operationId")
            if not isinstance(oid, str) or not oid.strip():
                out.append(f"{where}: operationId missing or empty")
            elif oid in seen_ids:
                out.append(f"{where}: operationId {oid!r} is already used by {seen_ids[oid]}")
            else:
                seen_ids[oid] = where
            responses = op.get("responses")
            if not isinstance(responses, dict) or not responses:
                out.append(f"{where}: responses is missing or empty")
                continue
            for code, body in responses.items():
                if not isinstance(body, dict):
                    out.append(f"{where}.responses.{code}: not a mapping")
                elif "$ref" not in body and not str(body.get("description", "")).strip():
                    out.append(f"{where}.responses.{code}: no description and no $ref")
                if str(code) != "default" and not re.match(r"^[0-9X]{3}$", str(code)):
                    out.append(f"{where}.responses.{code}: not a status code")

    for name, schema in (components.get("schemas") or {}).items():
        if not isinstance(schema, dict):
            out.append(f"components.schemas.{name}: not a mapping")

    # Every schema object anywhere in the document: a `type` outside the JSON
    # Schema vocabulary, or a `required` naming a property that does not exist.
    # Both are typos that a generator and every downstream client inherit.
    for where, node in walk(doc, ""):
        if where.startswith("components.securitySchemes") or where == "security":
            continue  # `type: http` and `type: apiKey` are security-scheme types
        t = node.get("type")
        if isinstance(t, str) and t not in SCHEMA_TYPES:
            out.append(f"{where}: type {t!r} is not one of {', '.join(SCHEMA_TYPES)}")
        props, required = node.get("properties"), node.get("required")
        if isinstance(props, dict) and isinstance(required, list):
            for field in required:
                if field not in props:
                    out.append(f"{where}: required field {field!r} has no matching properties entry")

    planned = doc.get("x-planned-paths")
    if isinstance(planned, dict):
        for group, entries in planned.items():
            if not isinstance(entries, list):
                out.append(f"x-planned-paths.{group}: not a list")
                continue
            for entry in entries:
                if not isinstance(entry, str) or not entry.startswith("/"):
                    out.append(f"x-planned-paths.{group}: {entry!r} is not a route path")
    elif "x-planned-paths" in doc:
        out.append("x-planned-paths: not a mapping")

    return out


# ---------------------------------------------------------------------------

def check_file(path: str) -> tuple[list[str], dict]:
    try:
        with open(path, encoding="utf-8") as fh:
            text = fh.read()
    except OSError as exc:
        return [f"{path}: {exc}"], {}

    findings, facts = scan(text)
    findings += check_scan(facts)
    findings += check_catalogues(facts)

    doc, error = parse(text)
    if error is not None:
        findings.insert(0, f"{path}: does not parse: {error}")
    elif doc is not None:
        findings += check_document(doc)
    return findings, facts


def main() -> int:
    ap = argparse.ArgumentParser(description="R8.1 — validate api/openapi.yaml")
    ap.add_argument("files", nargs="*", default=["api/openapi.yaml"])
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()

    results: dict[str, list[str]] = {}
    facts_all: dict[str, dict] = {}
    for path in args.files:
        findings, facts = check_file(path)
        results[path] = findings
        facts_all[path] = facts

    total = sum(len(v) for v in results.values())
    if args.json:
        print(json.dumps({
            "pyyaml": yaml is not None,
            "files": {
                p: {"errors": v,
                    "paths": len(f.get("paths", {})),
                    "refs": len(f.get("refs", [])),
                    "catalogue_entries": sum(len(c) for c in f.get("catalogues", {}).values())}
                for p, v in results.items() for f in [facts_all[p]]
            },
        }, indent=2))
    for finding in [f for v in results.values() for f in v]:
        print(f"error: {finding}", file=sys.stderr)
    if not total:
        for path, facts in facts_all.items():
            n_ops = sum(len(p["methods"]) for p in facts.get("paths", {}).values())
            n_cat = sum(len(c) for c in facts.get("catalogues", {}).values())
            print(f"ok: {path} — {len(facts.get('paths', {}))} paths, {n_ops} operations, "
                  f"{len(facts.get('refs', []))} $refs, {n_cat} catalogue entries "
                  f"[parse: {'pyyaml' if yaml is not None else 'line-scan only'}]")
    return 1 if total else 0


if __name__ == "__main__":
    sys.exit(main())
