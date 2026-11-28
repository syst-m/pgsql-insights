# Personas and jobs to be done

| # | Persona | Their job | Served by |
|---|---|---|---|
| P1 | **Platform engineer** running CNPG for several teams | "Install once, see every Postgres in the cluster, and know which one is on fire." | R1, R9, R10.1 (Fleet) |
| P2 | **Application developer** with one app on one cluster | "Why is *my* query slow, and what should I change?" | R3, R10.6–R10.7, R7 |
| P3 | **DBA / on-call** at 03:00 | "What is running right now, who is blocking whom, and can I kill it?" | R4, R10.8 |
| P4 | **SRE building a golden path** | "Give me the metrics in our central Prometheus and the alerts, without hand-maintaining a 66-panel dashboard." | R9, R11.7, R7 |
| P5 | **Evaluator / lab user** | "`helm install` in a namespace with one cluster and see something useful in under five minutes." | R11.6, R11.9, R13.6 |

**Jobs, in priority order** (this ordering is the phase order in §7):

1. J1 — See every CNPG cluster and its health at a glance. *(P1, P4)*
2. J2 — Get the top-N most expensive queries for a cluster, with text. *(P2, P3)*
3. J3 — Drill into one query: its history, plans, and per-minute load. *(P2)*
4. J4 — Export basic metrics to a central Prometheus and have them work across instances. *(P4)*
5. J5 — See what is running right now and what is blocking it. *(P3)*
6. J6 — See system-level health (CPU, memory, connections, TXID wraparound, disk, temp). *(P1, P4)*
7. J7 — Get actionable recommendations (index, bloat, rightsizing). *(P2)*
8. J8 — See pooler behaviour and correctly attribute work through pgbouncer. *(P2, P1)*

---

