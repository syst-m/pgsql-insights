{{/*
Naming and label helpers for the pgsi chart (R11.6, R11.7).
The chart is named `pgsi` to match the single binary (IMPLEMENTATION.md D3).
*/}}

{{- define "pgsi.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "pgsi.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "pgsi.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "pgsi.labels" -}}
helm.sh/chart: {{ include "pgsi.chart" . }}
{{ include "pgsi.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/part-of: pgsql-insights
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{- define "pgsi.selectorLabels" -}}
app.kubernetes.io/name: {{ include "pgsi.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "pgsi.annotations" -}}
{{- with .Values.commonAnnotations }}
{{- toYaml . }}
{{- end }}
{{- end -}}

{{/*
Container port. Defaults to the port in config.listen (R8.7) so the Deployment,
the Service and the probes can never disagree about where pgsi listens.
*/}}
{{- define "pgsi.containerPort" -}}
{{- if .Values.service.targetPort -}}
{{- .Values.service.targetPort -}}
{{- else -}}
{{- regexFind "[0-9]+$" (.Values.config.listen | toString) | int -}}
{{- end -}}
{{- end -}}

{{/*
R11.16 / R11.3: image reference. tag defaults to Chart.AppVersion; CI publishes
:dev plus the git-SHA tag, so an empty tag always resolves to a real image.
*/}}
{{- define "pgsi.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{- define "pgsi.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "pgsi.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
R11.12 — which named profile is in use. `default` (values.yaml), `production` or
`lab`; the two delta files set this key so NOTES.txt and the rendered labels can
name the profile that produced them.
*/}}
{{- define "pgsi.profile" -}}
{{- default "default" .Values.profile | toString -}}
{{- end -}}

{{- define "pgsi.pvcName" -}}
{{- default (printf "%s-data" (include "pgsi.fullname" .)) .Values.persistence.existingClaim -}}
{{- end -}}

{{/*
R12.1 — the least-privilege verb set, shared by the namespaced Role and the
cluster-scoped variant so the two can never drift (R12.3).
*/}}
{{- define "pgsi.rules" -}}
# --- CNPG clusters and their subresources (R1.2, R11.1) -------------------
- apiGroups: ["postgresql.cnpg.io"]
  resources:
    - clusters
    - clusters/status
    # poolers is here because internal/discovery watches them (R1.4's Pooler
    # discovery, feeding R8.2's PoolerView on the cluster row). Without it the
    # pooler watch on every namespace returns Forbidden, which R10.22 reports as
    # a failed namespace rather than a cluster with no poolers — so a missing
    # verb here looks like a broken cluster, not a missing permission.
    - poolers
    - poolers/status
    - backups
    - scheduledbackups
    - pooledqueries
    - clusterimages
  verbs: ["get", "list", "watch"]
# --- Workloads the product reads to map a cluster to its pods (R1.2, R5.x) --
- apiGroups: [""]
  resources: ["pods", "services", "configmaps", "events", "secrets"]
  verbs: ["get", "list", "watch"]
# --- R5.10: full Postgres logs. Inert while --log-source=off (R12.1). -----
- apiGroups: [""]
  resources: ["pods/log"]
  verbs: ["get", "list"]
# --- R1.6/R4.x: read a pod's HTTP endpoints (cnpg metrics exporter, pgbouncer)
# Grants read access to a pod's HTTP surface, not to pod contents (R12.9).
- apiGroups: [""]
  resources: ["pods/proxy"]
  verbs: ["create"]
# --- Owner references and the monitoring surface we read (R11.7) -----------
- apiGroups: ["apps"]
  resources: ["deployments"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["batch"]
  resources: ["jobs"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["policy"]
  resources: ["poddisruptionbudgets"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["discovery.k8s.io"]
  resources: ["endpointslices"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["monitoring.coreos.com"]
  resources: ["podmonitors", "servicemonitors"]
  verbs: ["get", "list", "watch"]
{{- if .Values.rbac.allowPodsExec }}
# R12.2 — pods/exec is code execution in the pod namespace, so it is NOT part of
# the default verb set; rbac.allowPodsExec=true opts in.
- apiGroups: [""]
  resources: ["pods/exec"]
  verbs: ["create"]
{{- end }}
{{- with .Values.rbac.extraRules }}
{{ toYaml . }}
{{- end }}
{{- end -}}
