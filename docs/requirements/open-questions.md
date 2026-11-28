# Open questions

| # | Question | Answer |
|---|---|---|
| Q1 | Product name and binary name — `pgsql-insights` / `pgsi`? | **Resolved.** Repo is `pgsql-insights`; binary and metric prefix are `pgsi`. Recorded as D3 in `IMPLEMENTATION.md`. |
| Q2 | Ship the SPA embedded, or also as a standalone static bundle for CDN hosting? | Embedded only in v1 (R8.6); `--serve-ui=false` covers API-only. |
| Q3 | Do we need `kubectl pgsi` as a plugin surface, mirroring `kubectl cnpg`? | Post-0.1. The API is the contract; a plugin is a thin client over it. |
| Q4 | Multi-user auth (per-user) vs. one shared token? | Shared token in v1 (R8.8); per-user via OIDC post-0.1. **Consequence:** R10.6's saved views are shared/cluster-scoped until OIDC lands. |
| Q5 | Should the store be Postgres-backed by default on CNPG clusters (dogfooding)? | No — SQLite default keeps the install dependency-free; Postgres-backed is the documented scale-up path (R11.15). |
| Q6 | Is Apache-2.0 correct, and does the org exist for `ghcr.io/<org>/`? | Apache-2.0 (R15.1). **The org name is still unresolved and is now a Phase 0 blocker** (A4-M9): R11.16 names image publication as the Phase 0 CI step that consumes it, and R11.9's quickstart cannot be copy-pasted until the registry exists. Until then, the packaged-tarball install is the verifiable form. |

---

