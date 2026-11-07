{{- define "nvr.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "nvr.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "nvr.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "nvr.selectorLabels" . }}
app.kubernetes.io/version: {{ (default .Chart.AppVersion .Values.image.tag) | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "nvr.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nvr.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "nvr.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "nvr.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/* Values the chart puts into its own Secret (only those given inline). */}}
{{- define "nvr.secretData" -}}
{{- if and (not .Values.webdav.existingSecret) .Values.webdav.password }}
webdav-password: {{ .Values.webdav.password | b64enc | quote }}
{{- end }}
{{- if and (not .Values.indexing.existingSecret) .Values.indexing.webhookSecret }}
ingest-secret: {{ .Values.indexing.webhookSecret | b64enc | quote }}
{{- end }}
{{- if and (eq .Values.auth.mode "oidc") (not .Values.auth.oidc.existingSecret) .Values.auth.oidc.clientSecret }}
oidc-client-secret: {{ .Values.auth.oidc.clientSecret | b64enc | quote }}
{{- end }}
{{- if and (not .Values.database.existingSecret) .Values.database.url }}
database-url: {{ .Values.database.url | b64enc | quote }}
{{- end }}
{{- end }}

{{/* Fail early on settings the app would reject at startup. */}}
{{- define "nvr.validate" -}}
{{- if not (has .Values.auth.mode (list "password" "oidc" "none")) }}
{{- fail (printf "auth.mode must be password, oidc or none (got %q)" .Values.auth.mode) }}
{{- end }}
{{- if eq .Values.auth.mode "oidc" }}
{{- if or (not .Values.auth.oidc.issuer) (not (or .Values.auth.oidc.clientID .Values.auth.oidc.existingSecret)) }}
{{- fail "auth.mode=oidc needs auth.oidc.issuer, and auth.oidc.clientID or auth.oidc.existingSecret" }}
{{- end }}
{{- if not .Values.auth.publicURL }}
{{- fail "auth.mode=oidc needs auth.publicURL (the address people open; the sign-in callback is <publicURL>/auth/oidc/callback)" }}
{{- end }}
{{- end }}
{{- if and .Values.webdav.url (not .Values.webdav.existingSecret) (or (not .Values.webdav.username) (not .Values.webdav.password)) }}
{{- fail "set webdav.existingSecret, or webdav.username and webdav.password" }}
{{- end }}
{{- if and .Values.indexing.webhookSecret (lt (len .Values.indexing.webhookSecret) 16) }}
{{- fail "indexing.webhookSecret must be at least 16 characters (try: openssl rand -hex 24)" }}
{{- end }}
{{- end }}
