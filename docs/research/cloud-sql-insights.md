# Cloud SQL for PostgreSQL — Observability Product Inventory

Reverse-engineering reference for building an open-source equivalent for PostgreSQL on
CloudNativePG (CNPG) / Kubernetes.

Compiled 2026-11-20. Everything below is transcribed from live Google Cloud documentation and
the live Cloud SQL Admin API discovery document, not from memory. Provenance is in §0.
Anything not directly evidenced is marked **(unverified)**.

---

## 0. Sources and verification

| Source | URL | What it yielded |
|---|---|---|
| Use query insights to improve query performance | `https://docs.cloud.google.com/sql/docs/postgres/using-query-insights` | §3.1–§3.9, §3.3 UI, normalization, pricing, editions table |
| Use system insights to improve system performance | `https://docs.cloud.google.com/sql/docs/postgres/use-system-insights` | §4.1–§4.2, §4.7 |
| Cloud SQL metrics (reference) | `https://docs.cloud.google.com/sql/docs/postgres/admin-api/metrics` | §4.3–§4.4, §3.7, §6.2 — full metric type strings, kinds, value types, units, all label enums |
| Cloud SQL Admin API guide | `https://docs.cloud.google.com/sql/docs/postgres/admin-api` | §5.1 resource list, discovery-vs-`sql` base path note |
| Cloud SQL Admin API discovery doc v1beta4 | `https://sqladmin.googleapis.com/$discovery/rest?version=v1beta4` (revision `20261107`, also cached copies at `sqladmin.v1beta4.json` in `google-api-python-client` tags v2.30.0/2.47.0/2.60.0/2.80.0/2.100.0/2.200.0) | §3.2, §5.2–§5.4 exact JSON field names |
| Cloud SQL Admin API discovery doc v1 | `https://sqladmin.googleapis.com/$discovery/rest?version=v1` | §5.2 (no Query* schemas either) |
| Monitor active queries | `https://docs.cloud.google.com/sql/docs/postgres/monitor-active-queries` | §3.10 |
| Monitor and troubleshoot Cloud SQL with AI assistance | `https://docs.cloud.google.com/sql/docs/postgres/monitor-troubleshoot-with-ai` | §3.11 |
| About database observability | `https://docs.cloud.google.com/sql/docs/postgres/observability` | §4.5 collection mechanism, §7 recommenders |
| Database flags | `https://docs.cloud.google.com/sql/docs/postgres/flags` | §3.6 `auto_explain.*`, `pg_stat_statements.*`, `track_*` |
| Managed Connection Pooling overview | `https://docs.cloud.google.com/sql/docs/postgres/managed-connection-pooling` | §6.1, §6.3, §6.4 |
| Configure Managed Connection Pooling | `https://docs.cloud.google.com/sql/docs/postgres/configure-mcp` | §6.1 REST/Terraform/gcloud shapes |
| Database Insights MCP server | `https://docs.cloud.google.com/sql/docs/postgres/reference/mcp/databaseinsights` (+ `get_query_metrics`, `get_system_metrics` tool pages) | §5.5 |
| Cloud SQL PostgreSQL MCP server | `https://docs.cloud.google.com/sql/docs/postgres/reference/mcp/postgres/mcp` | §5.5 tool list |
| Monitor Cloud SQL instances | `https://docs.cloud.google.com/sql/docs/postgres/monitor-instance` | §4.1, §4.3 (500-database note) |
| PostgreSQL features | `https://docs.cloud.google.com/sql/docs/postgres/features` | context |
| Pricing | `https://docs.cloud.google.com/sql/docs/postgres/pricing` | no insights line item (confirms "no additional cost") |

**Unverified items (explicit):**

1. **"Query and Locks" tab name.** The live Cloud SQL docs name the dashboard **"Query insights"**
   with sub-tabs **Queries / Wait event types / Wait events / Databases / Users / Tags /
   Client addresses** plus an **Active queries** tab. The string "Query and Locks" does not appear
   in the Cloud SQL docs set, nor in the AlloyDB docs set (AlloyDB's query insights dashboard has
   `QUERIES` and `TAGS` tables only). The name is most likely a console-only label or an AlloyDB
   console label. **(unverified)** — everything else about locks surfaces is documented and is
   inventoried in §3.3 (Lock Wait series, `insights/aggregate/lock_time` with `lock_type`
   `lw/hw/buffer_pin`) and §3.10 (blocked-query analysis).
2. **`queryConfigs` / `queryMetrics` / `queryViews` REST collections.** See §5.2 — they are not
   present in any reachable revision of the v1beta4 discovery document. Field names quoted for
   them in the task brief (`QueryMetadata`, `QueryStatistics`, `QueryString`, `QueryView`,
   `QueryPlanCache`, `PgQueryMetadata`) could not be confirmed from any Google source reachable
   today. **(unverified)**
3. **OS metric names in the brief** (`os.cpu.total/system/user/iowait`,
   `os.memory.anonymous/buffered/cached/slab/swap`, per-device/per-mountpoint disk IO) do not
   exist in Cloud SQL's metric set; Cloud SQL's equivalents are listed in §4.3, and the gap is
   called out in §4.6.

---

## 1. Product map

| Surface | What it is | Where |
|---|---|---|
| **Query Insights** | Query-level performance telemetry: normalized query list, load attribution, latency percentiles, query plans, traces, tags | Console tab `Query insights`; Cloud Monitoring metrics under `cloudsql.googleapis.com/database/postgresql/insights/*`; `settings.insightsConfig` in the Admin API |
| **Active queries** (sub-tab of Query insights) | Live `pg_stat_activity`-style view: active connections/transactions, longest-running transactions, terminate a session/transaction | Console `Query insights → Active queries`; `settings.geminiConfig.activeQueryEnabled` |
| **System Insights** | Instance/OS/DB health dashboards + events timeline + summary cards | Console tab `System insights`; Cloud Monitoring metrics under `cloudsql.googleapis.com/database/*` |
| **AI-assisted troubleshooting** (Preview) | Query anomaly detection + Gemini Cloud Assist investigations | Console; `settings.geminiConfig` |
| **Recommenders** | Out-of-disk, idle instance, over/under-provisioned, high transaction-ID utilization | Cloud Recommender |
| **Index advisor** (Enterprise Plus, Preview) | Index recommendations surfaced in Query insights | `settings.geminiConfig.indexAdvisorEnabled` |
| **Database Insights MCP server** | `get_query_metrics`, `get_system_metrics` (PromQL) | `https://databaseinsights.googleapis.com/mcp` |
| **Cloud SQL Admin API** | Instance CRUD + `insightsConfig`, `connectionPoolConfig`, `performanceCaptureConfig`, `geminiConfig`, `dataCacheConfig` | `sqladmin.googleapis.com` v1 / v1beta4 |
| **Cloud Logging** | `postgres.log`, `pgaudit.log` → `logging.googleapis.com/log_entry_count` | Cloud Logging |
| **Cloud Trace** | Query plan traces / end-to-end spans; **must be enabled** to view query plans and end-to-end views | Cloud Trace |

---

## 2. Editions matrix (Enterprise vs Enterprise Plus)

From the "Use query insights" comparison table:

| Aspect | Cloud SQL Enterprise | Cloud SQL Enterprise Plus |
|---|---|---|
| Supported PG versions | PostgreSQL 9.6+ | PostgreSQL 12+ |
| Machine types | All | Not supported on shared-core machine types or on read replica instances |
| Regions | Cloud SQL regional locations | Enterprise Plus regional locations |
| **Metrics retention** | **7 days** | **30 days** |
| **Query length limit** | max **4500 bytes** | max **100 KB** |
| **Query plan samples** | max **20**/min | max **200**/min |
| Wait event analysis | Not available | Available |
| Index advisor recommendations | Not available | Available |
| Terminate sessions / long-running transactions in active queries | Not available | Available |
| AI-assisted troubleshooting (Preview) | Not available | Available (Enterprise gets it only via Gemini Cloud Assist) |
| **Query cardinality limit** | aggregated statistics for up to the top **100** queries per day, keyed by the combination of client IP addresses, databases, application tag, usernames, and query text | up to the top **4,000** queries **per minute**, keyed by application tag, databases, usernames, and query ID |

Enterprise Plus extra capabilities (verbatim list): capture and analyze wait events for all
executed queries; filter aggregated load by extra dimensions (queries, tags, wait event types,
more); capture query plans for all executed queries; sample up to 200 query plans per minute;
capture query text up to 100 KB; near-real-time metrics (order of seconds); 30-day retention;
index advisor recommendations; terminate a session or long-running transaction in active queries;
AI-assisted troubleshooting (Preview).

---

## 3. Query Insights

### 3.1 Enablement and configuration

**Console path:** instance → `Configuration` tile → `Edit configuration` → expand
**`Query insights`** → **`Enable Query insights`**. Multi-instance: instance list →
`More Actions → Enable Query insights`.

Console feature checkboxes, per-edition availability and defaults:

| Checkbox | Enterprise | Enterprise Plus | Default |
|---|---|---|---|
| `Enable Enterprise Plus features` | Not available | Available | Disabled |
| `Active query analysis` | Available | Available | Disabled |
| `Index advisor recommendations` | Not available | Available | Disabled |
| `AI-assisted troubleshooting` | Not available | Available | Disabled |
| `Store client IP addresses` | Available | Available | Disabled |
| `Store application tags` | Available | Available | Disabled |
| `Customize query lengths` | 256–4500 bytes, default 1024 | 1024–100000, default 10000 | — |
| `Set the maximum sampling rate` | 0–20, default 5 | max 200, default 200 | — |

**gcloud:**

```
gcloud sql instances patch INSTANCE_ID \
  --insights-config-query-insights-enabled \
  --insights-config-record-client-address \
  --insights-config-record-application-tags \
  --insights-config-query-string-length=1024 \
  --insights-config-query-plans-per-minute=20
# disable: --no-insights-config-query-insights-enabled
```

**REST:**

```
PATCH https://sqladmin.googleapis.com/v1/projects/PROJECT_ID/instances/INSTANCE_ID
PATCH https://sqladmin.googleapis.com/sql/v1beta4/projects/{project}/instances/{instance}
{
  "settings": {
    "insightsConfig": {
      "queryInsightsEnabled": true,
      "recordClientAddress": true,
      "recordApplicationTags": true,
      "queryStringLength": 1024,
      "queryPlansPerMinute": 20
    }
  }
}
```
Response is an `sql#operation`.

**Terraform:**

```hcl
settings {
  insights_config {
    query_insights_enabled   = true
    query_string_length      = 1024
    record_application_tags  = true
    record_client_address    = true
    query_plans_per_minute   = 20
  }
}
```

**Restart rules:** PG maintenance version `20250302.00_RC30` or earlier + enabling Enterprise
Plus ⇒ instance restarts (several minutes; health checks, logging and monitoring and other
operations suspended). `20250302.00_RC31` or later ⇒ no restart. Enabling AI-assisted
troubleshooting alone ⇒ no restart. Enabling Managed Connection Pooling ⇒ database restart.

**Permissions:** `roles/databaseinsights.viewer` + `roles/cloudsql.viewer`. For Enterprise Plus,
custom roles and other predefined roles are insufficient. Terminating sessions additionally needs
`roles/cloudsql.editor` + `roles/databaseinsights.operationsAdmin`
(`databaseinsights.activeQueries.fetch`, `databaseinsights.activitySummary.fetch`,
`databaseinsights.performanceIssues.detect`, `databaseinsights.performanceIssues.investigate`).

**Cloud Trace API must be enabled** to view query plans and end-to-end views.

**Disable behavior:** "When you disable query insights, Cloud SQL doesn't delete any data from
disk."

### 3.2 `InsightsConfig` schema (cloudsqladmin v1beta4, verbatim)

`Settings.insightsConfig` — description: *"Insights configuration, for now relevant only for
Postgres."* Schema `InsightsConfig` — *"Insights configuration. This specifies when Cloud SQL
Insights feature is enabled and optional configuration."*

| JSON field | Type | Description (verbatim) |
|---|---|---|
| `queryInsightsEnabled` | `boolean` | Whether Query Insights feature is enabled. |
| `queryStringLength` | `integer (int32)` | Maximum query length stored in bytes. Default value: 1024 bytes. Range: 256-4500 bytes. Query lengths greater than this field value will be truncated to this value. When unset, query length will be the default value. Changing query length will restart the database. |
| `recordApplicationTags` | `boolean` | Whether Query Insights will record application tags from query when enabled. |
| `recordClientAddress` | `boolean` | Whether Query Insights will record client address when enabled. |
| `queryPlansPerMinute` | `integer (int32)` | Number of query execution plans captured by Insights per minute for all queries combined. Default is 5. |
| `enhancedQueryInsightsEnabled` | `boolean` | Optional. Whether enhanced query insights feature is enabled. |

Terraform names are the snake_case of these. Enterprise Plus overrides (query length to 100 KB,
sampling rate to 200/min) are documented in the console table above; the API field
`enhancedQueryInsightsEnabled` is the API-side switch for the enhanced/Enterprise-Plus variant.

### 3.3 Dashboard UI structure

**Enterprise Plus header controls:** `All queries` (shows database load for all queries for the
selected time range; each query individually color-coded), `Database`, `User`, `Client address`,
`Time range` (1 hour, 6 hours, 1 day, 7 days, 30 days, custom), `Wait event types` (filters by
CPU and Lock wait event types), and sort-by dimensions `Queries`, `Wait event types`,
`Databases`, `Users`, `Tags`, `Client addresses`.

**Enterprise header controls:** `Database`, `User`, `Client address`, `Time range` (same list),
`Database load graph`, the series toggles `CPU capacity`, `CPU and CPU wait`, `IO wait`,
`Lock wait`, and `Queries` / `Tags`.

**`Database load — all top queries` graph.** Definition: *"Database query load is a measure of
the work (in CPU seconds) that the executed queries in your selected database perform over time"*
— the ratio of time taken by all queries completed in a given time window to wall-clock time.
Series:

| Series | Definition |
|---|---|
| `CPU capacity` | Number of CPUs available on the instance (dark blue max-core line) |
| `CPU and CPU Wait` | Ratio of time taken by queries in an active state to wall-clock time. Includes time waiting for the Linux scheduler, so it can exceed the max-core line. IO and Lock waits do not block queries in active state. |
| `IO Wait` | Ratio of time queries spend waiting for IO to wall-clock. Includes **Read IO Wait** and **Write IO Wait** |
| `Lock Wait` | Ratio of time queries spend waiting for locks to wall-clock. Includes **Lock Waits**, **LwLock Waits**, **Buffer pin Lock waits** |

**`Top dimensions by database load` table — exact tabs and columns:**

| Tab | Columns |
|---|---|
| `Queries` | `Query`, `Database`, `Recommendations`, `Load by total time`, `Load by CPU`, `Load by IO wait`, `Load by lock wait`, `% load by Queries`, `Analyze latency` (AI, Preview), `Avg execution time (ms)`, `Total execution time (ms)`, `Avg rows returned`, `Times called` |
| `Wait event types` (EEP only) | `Avg time spent in wait (ms)`, `Total time spent in wait (ms)`, `Wait event type count`, `% load by SELECTED_DIMENSION` |
| `Wait events` (EEP only) | `Avg time spent in wait (ms)`, `Total time spent in wait (ms)`, `Wait event count`, `% load by SELECTED_DIMENSION` |
| `Databases` | `Avg time spent in database (ms)`, `Total time spent in database (ms)`, `% load by …` |
| `Users` | `Avg time spent in user (ms)`, `Total time spent in user (ms)`, `% load by …` |
| `Tags` | `Action`, `Controller`, `Framework`, `Route`, `Application`, `DB driver`, `Load by total time`, `Load by CPU`, `Load by IO wait`, `Load by lock wait`, `Avg execution time (ms)`, `Avg rows returned`, `Times called`, `Database` |
| `Client addresses` | `Total time spent in client address (ms)`, `% load by …` |

**Query list table (Enterprise edition) columns:** `Query` (normalized string; only the first
1024 characters are shown by default; entries labelled `UTILITY COMMAND` usually include `BEGIN`,
`COMMIT`, `EXPLAIN` or other wrapper commands), `Database`, `Recommendations` (e.g.
`Create indexes`), `Load by total time`, `Load by CPU`, `Load by IO wait`, `Load by lock wait`,
`% load by Queries`, `Analyze latency` (Preview), `Avg execution time (ms)`, `Times called`,
`Avg rows returned`.

Note on the `Avg execution time (ms)` column: the list "might not be exhaustive" — a query that is
the cause of high load for one user but not representative of that user's average behaviour may be
omitted from that user's entry.

### 3.4 Query details page (side panel / drill-down)

Reached by clicking a row in the `Queries` or `Tags` tab.

| View | Contents |
|---|---|
| `Database load — specific query` | Work in CPU seconds for the normalized query, computed from time taken by normalized queries completed at the **minute boundary** vs wall-clock. Header shows the **first 1024 characters** of the normalized query (literals removed). Filters: `Database`, `User`, `Client address`. Series: `CPU capacity`, `CPU and CPU wait`, `IO wait`, `Lock wait`. |
| `Database load — specific tags` | Same shape, aggregated over all queries carrying the selected tag set. |
| `Query plan samples` | Graph of all query plans running at particular times + duration of each. Right panel defaults to the **longest** sample. Each operation shows **latency**, **rows returned**, **cost**; selecting an operation adds **shared hit blocks**, **type of schema**, **loops**, **plan rows**. Sample rate is `queryPlansPerMinute`. |
| `End-to-end Trace` | Gantt chart of spans (records of individual operations) for the trace generated by the sampled query; span attributes/metadata on selection; `View in Cloud Trace` link to Trace Explorer. Requires Cloud Trace API. |
| `Latency` | **50th, 95th, 99th percentile** latencies for the normalized query. Latency of parallel queries is measured in wall-clock time even though load can be higher due to multiple cores. |

### 3.5 Query fingerprinting / normalization

- "Query insights stores and displays only normalized queries."
- PostgreSQL **9.6 and 10**: literal values replaced with `?`.
- PostgreSQL **11+**: literals replaced with `$1`, `$2`, … (documented example on an `UPDATE` of
  `"demo_customer"`).
- Query plan traces collect/store no constant values and remove PII that a constant might reveal.
- Active queries: a query running in different sessions is displayed as **different entries**
  (i.e. active-query rows are per-session, not merged by fingerprint).
- `query_hash` (metric label) is "a unique hash of the query string" — the closest documented
  analogue of a `queryId`. Cloud SQL's PG flag list does **not** expose `compute_query_id`
  (verified by grep of the full flags reference), so Cloud SQL's fingerprint is its own, not
  PG 14's native `compute_query_id`. **(unverified** whether the implementation uses it
  internally**)**.

### 3.6 Query plan capture (`auto_explain`)

Cloud SQL PG flags (from the flags reference; `Yes` in the last column = restart required):

| Flag | Type | Range / default | Restart |
|---|---|---|---|
| `cloudsql.enable_auto_explain` | boolean `on\|off` | default `off`; **Cloud SQL-specific flag** | Yes |
| `auto_explain.log_analyze` | boolean | default `off` | No |
| `auto_explain.log_buffers` | boolean | default `off` | No |
| `auto_explain.log_min_duration` | integer | `-1 … 2147483647`, default `-1` | No |
| `auto_explain.log_format` | string | `text\|xml\|json\|yaml`, default `text` | No |
| `auto_explain.log_level` | string | `debug5…debug\|info\|notice\|warning\|log`, default `log`; PG 12+ | No |
| `auto_explain.log_nested_statements` | boolean | default `off` | No |
| `auto_explain.log_settings` | boolean | default `off`; PG 12+ | No |
| `auto_explain.log_timing` | boolean | default `on` | No |
| `auto_explain.log_triggers` | boolean | default `off` | No |
| `auto_explain.log_wal` | boolean | default `off`; PG 13+ | No |
| `auto_explain.log_verbose` | boolean | default `off` | No |
| `auto_explain.sample_rate` | float | `0 … 1`, default `1` | No |

`pg_stat_statements` flags:

| Flag | Type | Range / default | Restart |
|---|---|---|---|
| `pg_stat_statements.max` | integer | `100 … 2147483647`, default **5000** | Yes |
| `pg_stat_statements.save` | boolean | default `on` | No |
| `pg_stat_statements.track` | enum `none\|top\|all` | default **top** | No |
| `pg_stat_statements.track_planning` | boolean | default `off`; PG 13+ | No |
| `pg_stat_statements.track_utility` | boolean | default `on` | No |

Related tracking flags: `track_activities` (`on`, no restart), `track_activity_query_size`
(`100 … 102400`, default 1 KB, restart), `track_counts` (`on`), `track_functions`
(`none|pl|all`, default `none`), `track_io_timing` (default `off`), `track_commit_timestamp`
(`off`, restart), `track_cost_delay_timing` (`off`, PG 18+).

Sampling/aggregation model: metrics are **sampled every 60 seconds** and, after sampling, **not
visible for up to 165 seconds** (all Cloud SQL metrics). Enterprise Plus makes query metrics
available **within 30 seconds** of query completion; Enterprise edition docs say "within minutes".
Query load is aggregated at the **minute boundary**.

### 3.7 Query Insights metrics (Cloud Monitoring) — exact names, kinds, units, labels

All with monitored resource **`cloudsql_instance_database`**, launch stage **GA**, scope
`(project)`. Three families × six metric types:

| Metric type string | Display name | Kind / Value / Unit | Labels | Description |
|---|---|---|---|---|
| `database/postgresql/insights/aggregate/execution_time` | Execution time | CUMULATIVE, INT64, `us{CPU}` | `user`, `client_addr` | Accumulated query execution time per user per database. Sum of cpu time, io wait time, lock wait time, process context switch, and scheduling for all processes involved in query execution. |
| `database/postgresql/insights/aggregate/io_time` | IO time | CUMULATIVE, INT64, `us` | `user`, `client_addr`, `io_type` (`read`/`write`) | Accumulated IO time per user per database. |
| `database/postgresql/insights/aggregate/latencies` | Latency | CUMULATIVE, **DISTRIBUTION**, `us` | `user`, `client_addr` | Query latency distribution per user per database (rendered as P50/P95/P99). |
| `database/postgresql/insights/aggregate/lock_time` | Aggregated lock time | CUMULATIVE, INT64, `us` | `user`, `client_addr`, `lock_type` (`lw`/`hw`/`buffer_pin`) | Accumulated lock wait time per user per database. |
| `database/postgresql/insights/aggregate/row_count` | Affected rows | CUMULATIVE, INT64, `1` | `user`, `client_addr` | Total number of rows affected during query execution. |
| `database/postgresql/insights/aggregate/shared_blk_access_count` | Shared blocks cache access | CUMULATIVE, INT64, `1` | `user`, `client_addr`, `access_type` (`hit`/`miss`) | Shared blocks (regular tables & indexes) accessed by statement execution. |
| `database/postgresql/insights/perquery/execution_time` | Per query execution times | CUMULATIVE, INT64, `us{CPU}` | `querystring` (the normalized query), `query_hash` (a unique hash of the query string), `user`, `client_addr` | Accumulated execution times per user per database per query. |
| `database/postgresql/insights/perquery/io_time` | Per query IO time | CUMULATIVE, INT64, `us` | `querystring`, `query_hash`, `user`, `client_addr`, `io_type` | Accumulated IO time per user per database per query. |
| `database/postgresql/insights/perquery/latencies` | Per query latency | CUMULATIVE, DISTRIBUTION, `us` | `querystring`, `query_hash`, `user`, `client_addr` | Query latency distribution per user per database per query. |
| `database/postgresql/insights/perquery/lock_time` | Per query lock time | CUMULATIVE, INT64, `us` | `querystring`, `query_hash`, `user`, `client_addr`, `lock_type` | Accumulated lock time per user per database per query. |
| `database/postgresql/insights/perquery/row_count` | Per query affected rows | CUMULATIVE, INT64, `1` | `querystring`, `query_hash`, `user`, `client_addr` | Rows affected per user per database per query. |
| `database/postgresql/insights/perquery/shared_blk_access_count` | Per query shared blocks access | CUMULATIVE, INT64, `1` | `querystring`, `query_hash`, `user`, `client_addr`, `access_type` | Shared block accesses per user per database per query. |
| `database/postgresql/insights/pertag/execution_time` | Per tag execution time | CUMULATIVE, INT64, `us{CPU}` | `action`, `application`, `controller`, `db_driver`, `framework`, `route`, `tag_hash`, `user`, `client_addr` | Accumulated execution time per tag combination. |
| `database/postgresql/insights/pertag/io_time` | Per tag IO time | CUMULATIVE, INT64, `us` | tag labels + `io_type` | Accumulated IO time per tag. |
| `database/postgresql/insights/pertag/latencies` | Per tag latency | CUMULATIVE, DISTRIBUTION, `us` | tag labels | Latency distribution per tag. |
| `database/postgresql/insights/pertag/lock_time` | Per tag lock time | CUMULATIVE, INT64, `us` | tag labels + `lock_type` | Accumulated lock time per tag. |
| `database/postgresql/insights/pertag/row_count` | Per tag affected rows | CUMULATIVE, INT64, `1` | tag labels | Rows affected per tag. |
| `database/postgresql/insights/pertag/shared_blk_access_count` | Per tag shared blocks access | CUMULATIVE, INT64, `1` | tag labels + `access_type` | Shared block accesses per tag. |

The doc's own summary line: *"The query insights metric type strings start with
`database/postgresql/insights`."*

Per-query metric set, in one list (with units): execution count (`Times called`, derived from the
CUMULATIVE counters), total execution time (`us{CPU}` / `Total execution time (ms)`), average
execution time (`Avg execution time (ms)`), max/percentile latency (`latencies` DISTRIBUTION in
`us` → P50/P95/P99), IO time (`us`, split read/write), lock time (`us`, split
lw/hw/buffer_pin), rows returned/affected (`1`), shared block accesses (`1`, hit/miss).
**Not present** as Cloud SQL metric fields: separate planning time, self-CPU time, parallel
worker counts, temp file/block counts per query (those exist only instance-wide, §4.4).

### 3.8 Tags (sqlcommenter / OpenTelemetry)

Accepted tag keys (all others are dropped): `action=''`, `controller=''`, `framework=''`,
`route=''`, `application=''`, `db driver=''`. Example:

```sql
SELECT * from USERS /*action='run+this',controller='foo%3',traceparent='00-01',tracestate='rojo%2'*/
```

Supported ORMs/frameworks: Python (Django, psycopg2, SQLAlchemy, Flask), Java (Hibernate, Spring),
Ruby (Rails), Node.js (Knex.js, Sequelize.js, Express.js), PHP (Laravel),
Go (http-tags, gorilla/mux, database/sql). Tag load is computed at the minute boundary using
wall-clock time. `traceparent`/`tracestate` carry OpenTelemetry context, which is what powers the
End-to-end Trace view.

### 3.9 Limits, quotas, retention, pricing

| Item | Value |
|---|---|
| Metrics retention | Enterprise **7 days**; Enterprise Plus **30 days** |
| Query string stored | default **1024 B**; Enterprise range **256–4500 B**; Enterprise Plus range **1024–100000 B** (default 10000); UI shows first 1024 chars |
| Query plan samples | Enterprise max **20/min** (default 5); Enterprise Plus max **200/min** (default 200) |
| Query cardinality | Enterprise top **100** queries/day (per client-IP × database × app-tag × user × query-text combination); Enterprise Plus top **4,000** queries/minute (per app-tag × database × user × query-ID) |
| Metric sampling | every **60 s**, visible after up to **165 s** |
| Metric freshness (EEP) | available within **30 s** of query completion |
| EEP storage cost of metrics | ~**36 GB for 7 days**, ~**155 GB for 30 days**; requires automatic storage increases enabled; applicable storage fees apply |
| EEP memory cost | up to **10 MB** of RAM (shared memory) |
| Price | **No additional cost** for query insights on either edition (Enterprise metrics live in Cloud Monitoring → Cloud Monitoring free tier / pricing applies) |
| Database-count limits (System Insights) | >**500** databases ⇒ single aggregated value for database metrics; `tuple_size` reported only when <**50** databases |
| Limitations | heavy system load ⇒ slow/timeout metric queries; restoring an older backup can lose metrics between backup and restore; with client-IP tracking enabled, a change in client IPs can cause gaps in charts (mitigate by restarting the instance or disabling client-IP tracking); PG 18 with maintenance version earlier than `20260319.00_RC02` may not store application tags when comment tags precede the SQL statement |
| pgbouncer interaction | with Managed Connection Pooling enabled, client IP tracking is unsupported and client IPs for ports **3307** and **6432** show as `local` |

### 3.10 Active queries tab

Availability: both editions for viewing; **Enterprise Plus only** for terminating a session or
long-running transaction. Enabled via `Configuration → Edit configuration → Query insights →
Active query analysis` (console only; not exposed via gcloud/REST, though
`settings.geminiConfig.activeQueryEnabled` is the output-only API mirror).

**Summary scorecard:** distribution of active connections by connection state; distribution of
active transactions by transaction state; distribution of query durations.

**`Longest running transactions` table** — top 50 running queries (states `active` and
`idle in transaction`), sorted by descending execution time; auto-refreshed every **60 seconds**;
columns:

| Column | Meaning |
|---|---|
| `Process ID` | Unique identifier of the connection or thread ID |
| `Query` | SQL query text |
| `Status` | Status of the connection |
| `Session duration` | Duration of the last session running |
| `Transaction duration` | Duration of the active transaction running |
| `Query duration` | Duration of the last query running in that transaction |
| `Wait event type` | Wait event type of the occurring wait event |
| `Wait event` | Occurring wait event |
| `Database` | Database name the connection is running on |
| `Application name` | Application name the connection is running on |
| `User name` | Connected user |
| `Client address` | Client IP that sent the query |
| `Action` | Link for terminating a transaction |

Column filtering via the "Content display options" menu. If there are many queries, results may be
limited to a subset.

**Blocked active queries (Preview):** requires Enterprise Plus + active queries +
`Blocked query analysis` checkbox. Expanding a process shows an **hourglass** icon (waiting for
another query; a number beside it = count of dependent queries waiting) or a **block** icon
(unable to finish, may be blocking others). The investigation tree expands **up to three levels
deep**. Also exposes `Query type` (e.g. `SELECT`, `UPDATE`).

**Terminate a process:** `Longest running transactions` → select row → `Action →
Terminate connection` → `Confirm`; performs a rollback if needed; long-running operations take
longer to terminate; if `Terminate` is greyed out, permissions are insufficient.

### 3.11 AI-assisted troubleshooting / anomaly detection (Preview)

- Requires Enterprise Plus (Enterprise gets it only via Gemini Cloud Assist), new network
  architecture, not inside a VPC-SC perimeter, not Access Transparency-enabled, not a read replica.
- As of 2026-04-10, Gemini Cloud Assist investigations require a **Premium Support** contract.
- Permissions: `roles/databaseinsights.viewer`,
  `roles/geminicloudassist.investigationOwner`; permissions
  `databaseinsights.performanceIssues.detect`, `databaseinsights.performanceIssues.investigate`.
- Detection model: **detection window = the 24 h preceding the end of the selected time range**;
  re-runs whenever the time range or any filter changes; recommended to wait 24 h after enabling to
  build a baseline.
- UI: a `warning_spark` icon next to `Avg execution time (ms)` in the `Top queries` table;
  `Query is slower than usual` dialog → `New Investigation` / `View all investigations`;
  `Actions → Investigate latency`; on the Query details page a "This query is slower than usual"
  banner and a `Query slower than usual` message in the latency chart.
- Investigation output (~2 min): **Issue** (description + start/stop time), **Observations**
  (e.g. lock-contention detail such as a longer-than-expected lock wait ratio), **Hypotheses**
  (recommended actions).
- `Analyze latency` column in the Top queries table is the same feature's entry point.

---

## 4. System Insights

### 4.1 UI

Console: instance → `System insights` tab. Sections: **instance details**, **Events timeline**,
**Summary cards**, **Metrics charts**.

Options: `Customize view` (1 or 2 charts per row; choose metrics); **Auto Refresh** (updates every
minute; incompatible with customized time periods); time selector default **1 day** with other
predefined periods + custom start/end; **data available for the last 30 days**; **Copy Link**;
**Notification** (create an alert for a specific event); **Annotations** (display specific alerts).

**Summary cards:**

| Card | Definition |
|---|---|
| `CPU utilization - P99 \| P50` | P99 and P50 CPU utilization over the selected period |
| `Peak connections` | Ratio of peak connections to max connections (can exceed max if max recently changed via scaling or `max_connections` change) |
| `Transaction ID utilization` | Latest transaction-ID utilization for the period |
| `Disk utilization` | Latest disk utilization |
| `Log errors` | Number of errors users log |

**Chart-card toolbar:** hover for values; drag-to-zoom (applies to all charts; `Reset zoom`);
More chart options → `View in full screen`, hide/collapse legend, **Download PNG or CSV**,
**View in Metrics Explorer**; `Customize dashboard` (create custom dashboards / `Predefined`
menu); `Explore data` (filter metrics, choose chart display) → `Save to dashboard`.

Also on the instance **Overview** page: a default metrics chart with a `Chart` drop-down, and
multi-instance comparison (up to **5** instances via checkboxes → Info Panel → `Monitoring` tab).

### 4.2 Default metrics on the System Insights dashboard (display name → metric type)

Prefix `cloudsql.googleapis.com/database/`. Note: instances with **>500 databases** report a
single aggregated value for database metrics.

| Display name | Metric type |
|---|---|
| New connections per second | `postgresql/new_connection_count` (PG 14+) |
| Wait event types | `postgresql/backends_in_wait` |
| Wait events | `postgresql/backends_in_wait` (shown as `wait event name:wait event type`) |
| Transaction count | `postgresql/transaction_count` |
| Memory components | `memory/components` |
| Max replica byte lag | `postgresql/external_sync/max_replica_byte_lag` |
| Query latency | `postgresql/insights/aggregate/latencies` (P99/P95/P50 per user and database; requires query insights) |
| Database load per database/user/client address | `postgresql/insights/aggregate/execution_time` |
| CPU utilization | `cpu/utilization` |
| Disk storage by type | `disk/bytes_used_by_data_type` (`data`, `wal`, `archived_wal_log`, `tmp_data`) |
| Disk read/write ops | `disk/read_ops_count`, `disk/write_ops_count` |
| Connections by status | `postgresql/num_backends_by_state` |
| Connections per database | `postgresql/num_backends` |
| Ingress/egress bytes | `network/received_bytes_count`, `network/sent_bytes_count` |
| I/O wait breakdown by type | `postgresql/insights/aggregate/io_time` (read/write) |
| Deadlock count by database | `postgresql/deadlock_count` |
| Block read count | `postgresql/blocks_read_count` |
| Rows processed by operation | `postgresql/tuples_processed_count` |
| Rows in database by state | `postgresql/tuple_size` (only if <50 databases) |
| Oldest transaction by age | `postgresql/vacuum/oldest_transaction_age` |
| WAL archiving | `replication/log_archive_success_count`, `replication/log_archive_failure_count` |
| Transaction ID utilization | `postgresql/transaction_id_utilization` |
| Connection count per application name | `postgresql/num_backends_by_application` (no app name ⇒ `Unknown`) |
| Rows fetched vs returned vs written | `postgresql/tuples_fetched_count`, `postgresql/tuples_returned_count`, `postgresql/tuples_processed_count` |
| Temp data size | `postgresql/temp_bytes_written_count` |
| Temp files | `postgresql/temp_files_written_count` |
| Log entries by severity | `logging.googleapis.com/log_entry_count` (from `postgres.log` and `pgaudit.log`) |

### 4.3 OS (generic) metric family — full list

All `cloudsql.googleapis.com/database/…`, **sampled every 60 s, visible after up to 165 s**,
monitored resource `cloudsql_database` unless noted.

| Metric | Kind / Value / Unit | Labels / notes |
|---|---|---|
| `database/cpu/utilization` | GAUGE, DOUBLE, `10^2.%` | Current CPU utilization as % of reserved CPU in use |
| `database/cpu/usage_time` | DELTA, DOUBLE, `s{CPU}` | |
| `database/cpu/reserved_cores` | GAUGE, DOUBLE | |
| `database/memory/utilization` | GAUGE, DOUBLE | |
| `database/memory/usage` | GAUGE, DOUBLE | Memory usage **excluding** buffer/cache |
| `database/memory/total_usage` | GAUGE, DOUBLE | Memory usage **including** buffer/cache |
| `database/memory/quota` | GAUGE, DOUBLE | |
| `database/memory/components` | **BETA** | label `component` ∈ [`usage`, `cache`, `free`]; `usage` = memory allocated by DB processes (incl. `shared_buffers` / InnoDB buffer pool), `cache` = OS page cache (reclaimable), `free` = unused; values as % of total memory |
| `database/swap/bytes_used` | GAUGE | |
| `database/swap/pages_swapped_in_count` | DELTA/CUMULATIVE | |
| `database/swap/pages_swapped_out_count` | DELTA/CUMULATIVE | |
| `database/disk/bytes_used` | GAUGE, INT64, `By` | |
| `database/disk/bytes_used_by_data_type` | GAUGE | label `data_type` (e.g. `data`, `wal`, `archived_wal_log`, `tmp_data`, `slow_log`) |
| `database/disk/quota` | GAUGE | |
| `database/disk/utilization` | GAUGE | |
| `database/disk/read_bytes_count` | DELTA, INT64, `By` | |
| `database/disk/write_bytes_count` | DELTA, INT64, `By` | |
| `database/disk/read_ops_count` | DELTA, INT64 | Reads served from disk not from cache |
| `database/disk/write_ops_count` | DELTA, INT64 | |
| `database/disk/provisioning/iops` | GAUGE | |
| `database/disk/provisioning/throughput` | GAUGE | label `io_type` |
| `database/disk/storage_auto_increase_limit` | GAUGE | |
| `database/data_cache/bytes_used` | GAUGE | |
| `database/data_cache/quota` | GAUGE | |
| `database/network/connections` | GAUGE | |
| `database/network/connection_attempt_count` | DELTA | |
| `database/network/received_bytes_count` | DELTA, INT64, `By` | |
| `database/network/sent_bytes_count` | DELTA, INT64, `By` | |
| `database/up` | GAUGE | |
| `database/uptime` | GAUGE | |
| `database/state` | GAUGE | **DEPRECATED** → use `database/instance_state` |
| `database/instance_state` | GAUGE | |
| `database/auto_failover_request_count` | DELTA | |
| `database/available_for_failover` | GAUGE | |
| `database/replication/state` | GAUGE | |
| `database/replication/replica_lag` | GAUGE | |
| `database/replication/network_lag` | GAUGE | |
| `database/replication/log_archive_success_count` | DELTA | |
| `database/replication/log_archive_failure_count` | DELTA | |
| `database/readpool/expected_pool_nodes` | GAUGE | |
| `semi_managed/database/*` | family | semi-managed variants of the above |

**Connector metrics (BETA, monitored resource `cloudsql.googleapis.com/InstanceClient`):**
`client/connector/closed_connection_count` (label `status` ∈ [`success`, `error`]),
`client/connector/connect_latencies` (CUMULATIVE, DISTRIBUTION, `ms`),
`client/connector/connection_durations` (CUMULATIVE, DISTRIBUTION, `s`),
`client/connector/open_connection_count` (labels `connection_part` ∈ [`client_to_proxy`,
`proxy_to_server`, `unknown`], `status` ∈ [`success`, `user_error`, `cache_error`, `tcp_error`,
`tls_error`, `auth_error`, `refresh_failed_error`, `refresh_expired_error`, `dns_lookup_error`,
`unknown_error`]), `client/connector/open_connections` — all with `instance_auth_type` ∈
[`built_in`, `iam`] and `instance_ip_type` ∈ [`public`, `private`, `psc`, `auto`, `sql_data`,
`unknown`].

### 4.4 DB (PostgreSQL) metric family — full list

Monitored resource `cloudsql_database` (insights metrics use `cloudsql_instance_database`).

| Metric | Kind / Value / Unit | Labels (enum values) |
|---|---|---|
| `database/postgresql/backends_in_wait` | GAUGE, INT64 | `backend_type` ∈ [autovacuum launcher, autovacuum worker, logical replication launcher, logical replication worker, parallel worker, background writer, client backend, checkpointer, archiver, startup, walreceiver, walsender, walwriter]; `wait_event` (e.g. `AutoVacuumMain`, `ClientRead`); `wait_event_type` ∈ [Activity, BufferPin, Extension, IO, IPC, Lock, LWLock, Timeout] |
| `database/postgresql/blocks_read_count` | DELTA, INT64 | `database`; `source` ∈ [disk, buffer_cache] |
| `database/postgresql/checkpoint_count` | DELTA, INT64 | `checkpoint_type` ∈ [scheduled, requested] |
| `database/postgresql/data_cache/hit_count` | GAUGE, INT64 | |
| `database/postgresql/data_cache/hit_ratio` | GAUGE, DOUBLE | |
| `database/postgresql/data_cache/miss_count` | GAUGE, INT64 | |
| `database/postgresql/deadlock_count` | DELTA | `database` |
| `database/postgresql/disk/read_bytes_count` | DELTA, INT64, `By` | **PG 16+ only**; `backend_type`, `object`, `context` |
| `database/postgresql/disk/write_bytes_count` | DELTA, INT64, `By` | **PG 16+ only**; `backend_type`, `object`, `context` |
| `database/postgresql/external_sync/initial_sync_complete` | ALPHA, GAUGE, BOOL | |
| `database/postgresql/external_sync/max_replica_byte_lag` | ALPHA, GAUGE, INT64, `By` | |
| `database/postgresql/new_connection_count` | DELTA | `database`; PG 14+ |
| `database/postgresql/num_backends` | GAUGE | `database` |
| `database/postgresql/num_backends_by_application` | GAUGE | `application` |
| `database/postgresql/num_backends_by_state` | GAUGE | `state` ∈ [idle, active, idle_in_transaction, idle_in_transaction_aborted, disabled, fastpath_function_call] |
| `database/postgresql/replication/replica_byte_lag` | GAUGE, INT64, `By` | `replica_name`; `replica_lag_type` ∈ [replay_location, flush_location, write_location, sent_location] |
| `database/postgresql/statements_executed_count` | DELTA | `database`, `operation_type` |
| `database/postgresql/temp_bytes_written_count` | DELTA, INT64, `By` | |
| `database/postgresql/temp_files_written_count` | DELTA, INT64 | |
| `database/postgresql/transaction_count` | DELTA | `database`, `transaction_type` ∈ [commit, rollback] |
| `database/postgresql/transaction_id_count` | DELTA | `action` ∈ [assigned, frozen] |
| `database/postgresql/transaction_id_utilization` | GAUGE, DOUBLE, `10^2.%` | |
| `database/postgresql/tuple_size` | GAUGE | `database`, `tuple_state` ∈ [live, dead] |
| `database/postgresql/tuples_fetched_count` | DELTA | |
| `database/postgresql/tuples_processed_count` | DELTA | `operation_type` ∈ [insert, update, delete] |
| `database/postgresql/tuples_returned_count` | DELTA | |
| `database/postgresql/vacuum/oldest_transaction_age` | GAUGE | `oldest_transaction_type` ∈ [running, prepared, replication_slot, replica] |
| `database/postgresql/write_ahead_log/flushed_bytes_count` | DELTA, INT64, `By` | |
| `database/postgresql/write_ahead_log/inserted_bytes_count` | DELTA, INT64, `By` | |
| `database/postgresql/write_ahead_log/redo_size` | GAUGE | |
| `database/postgresql/write_ahead_log/written_bytes_count` | DELTA, INT64, `By` | |
| `per_database/postgresql/external_sync/initial_sync_complete` | | |
| `per_database/postgresql/external_sync/replication_byte_lag` | | |

Plus the 18 `insights/*` metrics in §3.7.

### 4.5 Collection mechanism

- Cloud SQL uses **"built-in memory custom agents"** to collect query telemetry — in-process
  agents rather than an external agent/sidecar — "resulting in a lower impact on performance and
  eliminating the need for agent maintenance or security overhead" (About database observability).
- Query metrics come from the Query Insights implementation (PG hooks + `pg_stat_statements` +
  `auto_explain`), OS metrics from the instance's own agent publishing to Cloud Monitoring.
- Sampling: **every 60 seconds** for every metric; **up to 165 s** before data is visible.
- Real-time vs historical: System Insights `Auto Refresh` gives a 1-minute refresh (only with
  non-custom time ranges); Query Insights Enterprise Plus gives ~30 s freshness; the Active queries
  view refreshes every 60 s.
- Storage: Enterprise metrics live in Cloud Monitoring (no instance disk usage); Enterprise Plus
  stores query metrics on the instance's attached disk (36 GB/7 d, 155 GB/30 d).

### 4.6 Gap vs the metric list in the brief (OS detail)

The brief lists `os.cpu.total/system/user/iowait`, `os.memory.anonymous/buffered/cached/slab/swap`,
per-device and per-mountpoint disk IO. Verified by grep against the Cloud SQL metrics reference:
Cloud SQL exposes **no** CPU system/user/iowait split and **no** anonymous/buffered/slab memory
breakdown — only `memory/components` = `usage|cache|free`, plus `swap/*`. There is no per-device or
per-mountpoint disk dimension (disk metrics are instance-level, with `data_type` for space
accounting only). Those dotted names (`os.cpu.*`, `os.memory.*`) match **AlloyDB's** System
Insights metric vocabulary, not Cloud SQL's. For the OSS clone this is an *upgrade* opportunity:
on Kubernetes, cAdvisor/node-exporter give exactly the cpu system/user/iowait,
anonymous/buffered/slab, and per-device/per-mountpoint series that Cloud SQL lacks.

### 4.7 Events timeline (System Insights)

| Event | Operation type |
|---|---|
| Instance restart — restarts the instance | `RESTART` |
| Instance failover — manual failover of an HA primary to the standby | `FAILOVER` |
| Instance maintenance — typically 1–3 min unavailable | `MAINTENANCE` |
| Instance backup | `BACKUP_VOLUME` |
| Instance update — settings changed | `UPDATE` |
| Promote replica | `PROMOTE_REPLICA` |
| Start replica | `START_REPLICA` |
| Stop replica | `STOP_REPLICA` |
| Recreate replica | `RECREATE_REPLICA` |
| Create replica | `CREATE_REPLICA` |
| Data import | `IMPORT` |
| Instance export | `EXPORT` |
| Restore backup — may restart the instance | `RESTORE_VOLUME` |

The full `SQL_OPERATION_TYPE` enum from the discovery doc (superset, useful for an event feed):
`SQL_OPERATION_TYPE_UNSPECIFIED, IMPORT, EXPORT, CREATE, UPDATE, DELETE, RESTART, BACKUP,
SNAPSHOT, BACKUP_VOLUME, DELETE_VOLUME, RESTORE_VOLUME, INJECT_USER, CLONE, STOP_REPLICA,
START_REPLICA, PROMOTE_REPLICA, CREATE_REPLICA, CREATE_USER, DELETE_USER, UPDATE_USER,
CREATE_DATABASE, DELETE_DATABASE, UPDATE_DATABASE, FAILOVER, DELETE_BACKUP, RECREATE_REPLICA,
TRUNCATE_LOG, DEMOTE_MASTER, MAINTENANCE, ENABLE_PRIVATE_IP, DEFER_MAINTENANCE, CREATE_CLONE,
RESCHEDULE_MAINTENANCE, START_EXTERNAL_SYNC, LOG_CLEANUP, AUTO_RESTART, REENCRYPT, SWITCHOVER,
UPDATE_BACKUP, ACQUIRE_SSRS_LEASE, RELEASE_SSRS_LEASE, RECONFIGURE_OLD_PRIMARY,
CLUSTER_MAINTENANCE, SELF_SERVICE_MAINTENANCE, SWITCHOVER_TO_REPLICA, MAJOR_VERSION_UPGRADE,
ADVANCED_BACKUP, MANAGE_BACKUP, ENHANCED_BACKUP, REPAIR_READ_POOL, CREATE_READ_POOL,
PRE_CHECK_MAJOR_VERSION_UPGRADE, CREATE_BLUE_GREEN_DEPLOYMENT, SWITCHOVER_BLUE_GREEN_DEPLOYMENT,
DELETE_BLUE_GREEN_DEPLOYMENT, SETUP_MIGRATION, AGENT_SEND_MESSAGE`.
`Operation.status` ∈ [`SQL_OPERATION_STATUS_UNSPECIFIED`, `PENDING`, `RUNNING`, `DONE`].

---

## 5. Cloud SQL Admin API (cloudsqladmin) v1beta4

### 5.1 Current resources

Discovery doc: `name=sqladmin`, `version=v1beta4`, `revision=20261107`,
`rootUrl=https://sqladmin.googleapis.com/`, `mtlsRootUrl=https://sqladmin.mtls.googleapis.com/`,
`basePath` = `/sql/` (the docs note: Discovery uses `sqladmin`, but the REST base path is `sql` —
`https://www.googleapis.com/sql/v1beta4/projects/...`). 160 schemas.

Resources: `backupRuns`, `backups`, `blueGreenDeployments`, `connect`, `databases`, `flags`,
`instances`, `operations`, `projects`, `sslCerts`, `tiers`, `users`, `workloadCaptures`.
(`v1` has the same set minus `blueGreenDeployments`/`workloadCaptures`, plus legacy capitalized
`Backups` pages.)

`instances` methods: `reencrypt, clone, RotateServerCertificate, promoteReplica,
acquireSsrsLease, demote, listServerCas, ListEntraIdCertificates, stopReplica, list,
preCheckMajorVersionUpgrade, pointInTimeRestore, restoreBackup, truncateLog, demoteMaster, export,
addEntraIdCertificate, addServerCertificate, resetSslConfig, import, addServerCa, restart,
executeSql, releaseSsrsLease, rotateServerCa, update, patch, RotateEntraIdCertificate,
startReplica, switchover, ListServerCertificates, failover, get, insert, delete`.

### 5.2 `queryConfigs` / `queryMetrics` / `queryViews` — status

**They do not exist in the current API.** Verified against:

- live v1beta4 discovery doc (revision `20261107`) — resource list above, and `grep` of all 160
  schema names for `Quer` → **zero** matches;
- live v1 discovery doc — also zero `Quer` schemas;
- the docs REST index (`/sql/docs/postgres/admin-api/rest/v1beta4`, 93 pages) — no query* pages;
- cached discovery docs from `google-api-python-client` tags v2.30.0 (`20211023`), v2.47.0
  (`20220425`), v2.60.0 (`20220824`), v2.80.0 (`20230211`), v2.100.0 (`20230831`), v2.200.0
  (`20260722`) — none contain query* resources or Query* schemas;
- `google-api-dotnet-client` v1.60.0, v1.68.0, current — no `Query*` classes in
  `Google.Apis.SQLAdmin.v1beta4`;
- AlloyDB v1/v1beta/v1alpha discovery docs — the only Query schema is `QueryInsightsInstanceConfig`
  (the AlloyDB analogue of `InsightsConfig`).

So the field names in the brief (`QueryMetadata`, `QueryStatistics`, `QueryString`, `QueryView`,
`QueryPlanCache`, `PgQueryMetadata`) are **(unverified)** — no Google source reachable today
defines them. The programmatic Query Insights surface that *does* exist is:
`settings.insightsConfig` (§3.2), the Cloud Monitoring metrics (§3.7), and the Database Insights
MCP API (§5.5).

### 5.3 REST paths (exact)

| Method | Path | Params |
|---|---|---|
| `instances.get` | `GET sql/v1beta4/projects/{project}/instances/{instance}` | `instance`, `location`, `project` |
| `instances.patch` | `PATCH sql/v1beta4/projects/{project}/instances/{instance}` | `instance`, `project`, `location`, `reconcilePscNetworking`, `reconcilePscNetworkingForce` |
| `instances.update` | `PUT sql/v1beta4/projects/{project}/instances/{instance}` | `project`, `instance`, `location` |
| `instances.list` | `GET sql/v1beta4/projects/{project}/instances` | `project`, `location`, `filter`, `pageToken`, `maxResults` |
| `instances.restart` | `POST sql/v1beta4/projects/{project}/instances/{instance}/restart` | `location`, `project`, `instance` |
| `workloadCaptures.list` | `sql/v1beta4/projects/{project}/instances/{instance}/workloadCaptures` | (+ `start`, `stop`, `startReplay`, `stopReplay`) |
| v1 alias (documented) | `PATCH https://sqladmin.googleapis.com/v1/projects/PROJECT_ID/instances/INSTANCE_ID` | |

### 5.4 Related schemas (exact field names)

**`DatabaseInstance`** (the instance resource; `kind: sql#instance`):
`backendType, upgradableDatabaseVersions, currentDiskSize, masterInstanceName, maxDiskSize,
onPremisesConfiguration, gceZone, diskEncryptionConfiguration, kind, deploymentInfo,
databaseCenterIntegration, etag, replicationCluster, secondaryGceZone, serviceAccountEmailAddress,
failoverReplica, satisfiesPzs, dnsName, selfLink, databaseVersion, skipPrecheck, rootPassword,
geminiConfig, primaryDnsName, ipv6Address, project, outOfDiskReport, state,
includeReplicasForMajorVersionUpgrade, databaseInstalledVersion, scheduledMaintenance,
availableMaintenanceVersions, dnsNames, switchTransactionLogsToCloudStorageEnabled, settings,
createTime, satisfiesPzi, replicaConfiguration, tags, maintenanceVersion, nodeCount,
connectionName, sqlNetworkArchitecture, diskEncryptionStatus, pscServiceAttachmentLink,
instanceType, suspensionReason, region, replicaNames, databaseCenterIntegrationEnabled,
writeEndpoint, name, ipAddresses, nodes, serverCaCert`.

**`Settings`** (`kind: sql#settings`) — where the observability configs live:
`autoUpgradeEnabled, locationPreference, crashSafeReplicationEnabled, entraidConfig,
databaseReplicationEnabled, backupConfiguration, enableGoogleMlIntegration, timeZone,
dataDiskSizeGb, dataCacheConfig, passwordValidationPolicy, dataDiskType,
acceleratedReplicaMode, collation, maintenanceWindow, databaseFlags, pricingPlan,
replicationType, availabilityType, userLabels, authorizedGaeApplications,
dataDiskProvisionedThroughput, settingsVersion, sqlServerAuditConfig, finalBackupConfig,
replicationLagMaxSeconds, denyMaintenancePeriods, readPoolAutoScaleConfig,
enableDataplexIntegration, performanceCaptureConfig, ipConfiguration, connectionPoolConfig,
activationPolicy, connectorEnforcement, insightsConfig, retainBackupsOnDelete, dataApiAccess,
deletionProtectionEnabled, dataDiskProvisionedIops, edition, activeDirectoryConfig,
storageAutoResizeLimit, tier, kind, storageAutoResize, advancedMachineFeatures`.

**`Operation`** (`kind: sql#operation`): `status, name, operationType,
startWorkloadReplayContext, subOperationType, insertTime, startTime, exportContext,
targetProject, targetLink, preCheckMajorVersionUpgradeContext, stopWorkloadCaptureContext,
selfLink, backupContext, error, startWorkloadCaptureContext, apiWarning, user,
stopWorkloadReplayContext, targetId, importContext, endTime, acquireSsrsLeaseContext, kind`.

**`PerformanceCaptureConfig`** (Enterprise Plus "Performance Capture" — diagnostic capture during
high load; the MySQL-leaning sibling of Query Insights, but exposed for the instance):

| Field | Type | Notes |
|---|---|---|
| `enabled` | boolean | Enables/disables performance capture |
| `cpuUtilizationThresholdPercent` | int32 | 10–99, `0` disables the check |
| `memoryUsageThresholdPercent` | int32 | 10–99, `0` disables |
| `probeThreshold` | int32 | Min consecutive probes that trigger capture |
| `probingIntervalSeconds` | int32 | Interval between probes |
| `runningThreadsThreshold` | int32 | Min MySQL `Threads_running` to trigger on primary |
| `secondsBehindSourceThreshold` | int32 | Min replica lag (s) to trigger capture on replica |
| `historyListLengthThresholdCount` | int32 | 10000–10000000 undo-log history list length, `0` disables |
| `semaphoreWaitThresholdCount` | int32 | 10–10000 semaphore waits, `0` disables |
| `transactionDurationThreshold` | int32 | Seconds a transaction must be open before the watcher records it |
| `transactionKillThresholdSeconds` | int32 | 60–604800 (7 d), `0` disables; must be ≥ `transactionDurationThreshold` |
| `transactionKillType` | string enum | [`TRANSACTION_KILL_TYPE_UNSPECIFIED`, `READ_ONLY_TRANSACTIONS`, `ALL_TRANSACTIONS`]; default `READ_ONLY_TRANSACTIONS` |
| `transactionKillExcludedUserHosts` | array | `'user@host'` or `'user'` (implies `user@%`); wildcard `%` allowed in host part |
| `transactionLockWaitThresholdCount` | int32 | 10–10000 transactions in lock-wait to trigger, `0` disables |

**`GeminiInstanceConfig`** (`settings.geminiConfig`, all Output only booleans):
`entitled` (whether Gemini is enabled), `activeQueryEnabled`, `indexAdvisorEnabled`,
`flagRecommenderEnabled`, `googleVacuumMgmtEnabled`, `oomSessionCancelEnabled`.
→ These are the API mirrors of the console checkboxes `Active query analysis`,
`Index advisor recommendations`, `AI-assisted troubleshooting`.

**`DataCacheConfig`**: `dataCacheEnabled` (boolean).

**`ConnectPoolNodeConfig` / `PoolNodeConfig`** (read-pool nodes): `dnsName`, `dnsNames`,
`ipAddresses`, `name`, `gceZone`, `pscAutoConnections`, `pscServiceAttachmentLink`, `state` ∈
[`SQL_INSTANCE_STATE_UNSPECIFIED`, `RUNNABLE`, `SUSPENDED`, `PENDING_DELETE`, `PENDING_CREATE`,
`MAINTENANCE`, `FAILED`, `ONLINE_MAINTENANCE`, `REPAIRING`].

**`WorkloadCapture`**: `sourceInstance, replayInstance, workloadId, startTime, retentionDays,
backupId, endTime, workloadCaptureState`.

### 5.5 Database Insights MCP API (the current "Query Insights API")

Server: **`databaseinsights.googleapis.com`**, endpoint **`https://databaseinsights.googleapis.com/mcp`**.
Tools (both annotated Destructive ❌, Idempotent ✅, Read Only ✅, Open World ❌):

| Tool | Request | Response |
|---|---|---|
| `get_query_metrics` | `FetchQueryMetricsRequest`: `parent` (`projects/{project}/locations/{location}`), `resource` (project ID), `promqlQuery`, `startTime` (RFC3339), `endTime` (RFC3339) | `FetchQueryMetricsResponse`: `promqlResult` (string) |
| `get_system_metrics` | `FetchSystemMetricsRequest`: same five fields | `FetchSystemMetricsResponse`: `promqlResult` (string) |

This is the strongest signal for the shape of a modern API: **PromQL-in / PromQL-result-out** over
the same metric set, rather than per-resource REST collections.

Separate, the **Cloud SQL PostgreSQL MCP server**
(`docs.cloud.google.com/sql/docs/postgres/reference/mcp/postgres/mcp`) exposes:
`clone_instance, create_backup, create_instance, create_user, execute_sql, execute_sql_readonly,
get_instance, get_operation, import_data, list_instances, list_users,
postgres_upgrade_precheck, restore_backup, update_instance, update_user`.

---

## 6. Connection pooling (pgbouncer) insights

Cloud SQL's managed pooling is pgbouncer-based ("Managed Connection Pooling", MCP).

### 6.1 Configuration surface

`Settings.connectionPoolConfig` → schema **`ConnectionPoolConfig`**:

| Field | Type | Description |
|---|---|---|
| `connectionPoolingEnabled` | boolean | Whether managed connection pooling is enabled |
| `flags` | array of `ConnectionPoolFlags` | Optional. List of connection pool configuration flags |
| `poolerCount` | integer (int32) | **Output only.** Number of connection poolers |

**`ConnectionPoolFlags`**: `name` (string, Required), `value` (string, Required — *"Boolean flags
are set to `on` for true and `off` for false. This field must be omitted if the flag doesn't take a
value."*).

REST: `PATCH https://sqladmin.googleapis.com/v1/projects/PROJECT_ID/instances/INSTANCE_ID` with
`{"settings":{"connectionPoolConfig":{"connectionPoolingEnabled":true}}}` and, for flags,
`{"settings":{"connectionPoolConfig":{"flags":[{"name":"max_pool_size","value":"10"}]}}}`.
gcloud: `--connection-pool-flags=CONFIGURATION_NAME=CONFIGURATION_VALUE`.
Terraform: `connection_pool_config { connection_pooling_enabled = true \n flags { name = "max_pool_size" value = "10" } }`.

Pool flags and defaults (all applied **per connection pooler**):

| Flag | Default / range |
|---|---|
| `pool_mode` | `transaction` (default) or `session` |
| `max_pool_size` | 50 server connections per database+user pair, per pooler |
| `min_pool_size` | 0 |
| `max_client_connections` | 5,000 per pooler |
| `max_prepared_statements` | 0 (0 disables prepared-statement support) |
| `client_connection_idle_timeout` | 0 s (range 0–2,147,483 s) |
| `server_connection_idle_timeout` | 600 s (range 0–2,147,483 s) |
| `query_wait_timeout` | 120 s (0 = indefinite queuing; range 0–2,147,483 s) |
| `ignore_startup_parameters` | — |
| `server_lifetime` | 3600 s |

Pooler count scales as **vCPUs ÷ 4, minimum 1** (2–4 vCPU ⇒ 1, 8 ⇒ 2, 16 ⇒ 4, 32 ⇒ 8, 64 ⇒ 16).
Requirements: Enterprise Plus only; direct connection or Cloud SQL Auth Proxy only; private
service access / public IP / PSC; new network architecture; maintenance version
`POSTGRES_$version.R20250727.00_14`+. Ports: **5432** (direct PG), **6432** (direct to pooler),
**3307** (Auth Proxy to pooler). Transaction-mode unsupported SQL: `SET/RESET`, `LISTEN`,
`WITH HOLD CURSOR`, `PREPARE/DEALLOCATE`, `PRESERVE/DELETE ROW` temp tables, `LOAD`, session-level
advisory locks.

### 6.2 Pooling metrics (exact names)

Monitored resource `cloudsql_instance_database`; prefix `cloudsql.googleapis.com/per_database/`:

| Metric | Kind / Value / Unit | Labels / description |
|---|---|---|
| `per_database/conn_pool/client_connections` | GAUGE, INT64 | `status` (e.g. `active`, `waiting`) |
| `per_database/conn_pool/client_connections_attempts_count` | CUMULATIVE, INT64 | |
| `per_database/conn_pool/client_connections_avg_wait_time` | GAUGE, INT64, `us` | "Average wait time spent by clients waiting for a server connection per database in microseconds" |
| `per_database/conn_pool/client_connections_error_count` | CUMULATIVE, INT64 | "Count of errors that cause client disconnection including auth error, client connection setup error, server connection error and timeout errors" |
| `per_database/conn_pool/num_pools` | GAUGE, INT64 | "Number of connection pools per database" |
| `per_database/conn_pool/server_connections` | GAUGE, INT64 | `status` (e.g. `active`, `idle`) |

(MySQL equivalents exist at `database/mysql/conn_pool/{avg_wait_time, pending_connections, threads}`.)

### 6.3 Effect on query attribution (the pgbouncer problem, and Google's answer)

- **Cloud SQL's answer: it disables client-IP attribution rather than mis-attribute it.**
  "Client IP tracking isn't supported with Managed Connection Pooling. If you enable store client
  IP addresses in query insights, then client IP addresses are displayed as **`local`** instead of
  the IP address itself" — for connections on ports **3307** and **6432** (i.e. connections that
  went through the pooler). Port 5432 (direct) traffic keeps real client addresses.
- Query attribution otherwise stays correct because pgbouncer multiplexes at the **connection**
  level while `pg_stat_statements` attributes at the **session/backend** level: the server
  connection's `pg_stat_activity` row still carries the real `usename`, `datname`,
  `application_name`, so `user`/`database`/`application` dimensions survive; only the client
  address is lost (the pooler's socket is the peer).
- Corollary for the OSS clone: with transaction pooling, one server backend serves many logical
  sessions, so per-backend attribution of *sessions* breaks even though per-query attribution via
  `pg_stat_statements` does not. Cloud SQL's documented mitigation (show `local`) is the minimal
  honest answer; a better one is to record the pgbouncer-side client address from the pooler's own
  stats (`SHOW CLIENTS` / `SHOW POOLS`) and attach it as a separate dimension.
- Related documented caveat (AlloyDB docs, same product family): with client-IP tracking enabled
  and >100 clients, only the client addresses contributing the highest cumulative execution time
  are captured, and changes in client traffic cause temporary metric gaps while the new top
  addresses are exported (explicitly mentions Kubernetes pods restarting).

---

## 7. Related products needed for parity

| Product | What it provides | Notes |
|---|---|---|
| **Recommenders** | Out-of-disk recommender; Idle instance recommender; Overprovisioned instance recommender; Underprovisioned instance recommender; **High transaction ID utilization recommender** (PG-specific) | Cloud Recommender; heuristic + ML |
| **Index advisor** (EEP, Preview) | `Create indexes` recommendations shown in the Query list `Recommendations` column | `settings.geminiConfig.indexAdvisorEnabled` |
| **Flag recommender** | `settings.geminiConfig.flagRecommenderEnabled` (output only) | |
| **Google vacuum management** | `settings.geminiConfig.googleVacuumMgmtEnabled` | |
| **OOM session cancel** | `settings.geminiConfig.oomSessionCancelEnabled` | |
| **Performance Capture** | Trigger-based diagnostic capture on high load + transaction kill policy | `settings.performanceCaptureConfig` (§5.4) |
| **Cloud Logging** | `postgres.log`, `pgaudit.log` → `logging.googleapis.com/log_entry_count` by severity | |
| **Cloud Trace** | Query plan traces + end-to-end spans; enabling is a hard prerequisite for the plans/trace views | |
| **Alerting** | Alert policies on any Cloud SQL metric; per-event `Notification` button on System Insights; `Annotations` to display alerts on charts; Slack/PagerDuty/email/SMS via Cloud Monitoring channels | |
| **Multi-instance compare** | Up to 5 instances compared on one chart (Info Panel → Monitoring) | |
| **Custom dashboards** | `Customize dashboard` per chart; `Predefined` menu; `Save to dashboard` from Explore data | |
| **Export** | Per-chart **Download PNG or CSV**; `View in Metrics Explorer` | |

---

## 8. Parity checklist (capability → Cloud SQL source → PG/CNPG source)

| Cloud SQL capability | Google's source | Source for the CNPG/K8s clone |
|---|---|---|
| Query list + fingerprint + per-query stats | Query Insights agent (PG hooks + `pg_stat_statements`) | `pg_stat_statements` (`SELECT`, `calls`, `total_exec_time`, `min/max/mean/stddev_exec_time`, `total_plan_time`, `rows`, `shared_blks_hit/read/dirtied/written`, `local_blks_*`, `temp_files`, `temp_bytes`, `jit_*`, `wal_records/bytes`, plus PG 14+ `queryid` from `compute_query_id`) |
| Query text + truncation | `queryStringLength` 1024 default (256–4500 / up to 100 KB) | `pg_stat_activity.query`, `pg_stat_statements.queryid` + `pg_stat_statements` text store; configurable truncation |
| Normalization | literals → `?` (PG ≤10) / `$1,$2` (PG 11+) | normalize at ingest; keep both raw sample and digest |
| Load attribution (CPU/IO/lock) | `insights/aggregate/{execution_time,io_time,lock_time}` + `perquery/*` + `pertag/*` | `pg_stat_statements` exec/plan time + `blk_read_time`/`blk_write_time` (`track_io_timing`) + `pg_stat_activity.wait_event_type` |
| Wait events | `postgresql/backends_in_wait` (`wait_event_type` enum) | `pg_stat_activity.wait_event` / `wait_event_type` (PG 9.6+ event names) |
| Latency percentiles | `insights/aggregate/latencies` DISTRIBUTION in `us` | histogram buckets from `pg_stat_statements` (or store per-call samples to get true P50/P95/P99) |
| Query plans | `auto_explain` + `queryPlansPerMinute` (5 default, 20 EE, 200 EEP) | `auto_explain.log_min_duration`/`sample_rate`/`log_analyze`, store JSON plans per `queryid` |
| End-to-end trace | sqlcommenter `traceparent` + Cloud Trace | sqlcommenter / OpenTelemetry SQL instrumentation + Tempo/Jaeger |
| Tags | sqlcommenter keys `action, controller, framework, route, application, db driver` | parse the comment; store as labels (`pertag` equivalent) |
| Active queries | Active queries tab (top 50, 60 s refresh, terminate) | `pg_stat_activity` full dump + `pg_terminate_backend()`; blocked-query tree from `pg_locks` (`pg_blocking_pids`) |
| Locks | `insights/aggregate/lock_time` (`lw/hw/buffer_pin`), Lock Wait series | `pg_locks` + `pg_blocking_pids()`; LWLock breakdown needs `pg_stat_activity` (PG 16+ `wait_event` names) |
| OS metrics | `database/{cpu,memory,disk,network,swap}/*` (60 s sampling) | cAdvisor / node-exporter / `procfs` — gives *more* than Cloud SQL (cpu system/user/iowait, memory anonymous/buffered/slab, per-device IO) |
| DB metrics | `database/postgresql/*` full list in §4.4 | `postgres_exporter` metric set (same `pg_stat_database`/`pg_stat_bgwriter`/`pg_stat_statements` sources) |
| Events timeline | `sql#operation` types (§4.7) | CNPG `Cluster` events + `kubectl get events` + CNPG cluster transactions / `pg_stat_replication` changes |
| HA / replication | `postgresql/replication/replica_byte_lag` (`replica_lag_type` enum), `external_sync/*`, `replication/state`, `available_for_failover` | `pg_stat_replication` (`pg_wal_lsn_diff` for flush/replay/write/sent), CNPG instance roles/labels |
| Retention | 7 d (EE) / 30 d (EEP) query metrics; 30 d system metrics | configurable retention + downsampling |
| API | `settings.insightsConfig` + Cloud Monitoring + `databaseinsights.googleapis.com` MCP (PromQL in/out) | REST + a PromQL-compatible query endpoint over the store; MCP tool pair mirroring `get_query_metrics` / `get_system_metrics` |
| Pooling insights | `per_database/conn_pool/*` + `ConnectionPoolConfig` + `local` client-IP rule | pgbouncer `SHOW POOLS/CLIENTS/SERVERS` (CNPG pgbouncer cluster) → the six `conn_pool/*` equivalents |
| AI/anomaly | 24 h detection window, warning icons, Gemini investigations | baseline + anomaly detection per `queryid`; optional LLM explanation layer |
