---
type: operations
title: Deployment
description: How to run webdav-nvr-player in production, covering the multi-arch container image, the docker-compose example, the Helm chart (single replica, Recreate strategy, PVC, Secrets, Ingress or HTTPRoute, probes, hardened security context), the data volume, and admin password recovery in a container.
tags: [deployment, docker, helm, kubernetes, operations]
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T01:57:23.266Z
sources:
  - id: openwiki-source-f9b841a3dfc91dd1c38f3b50
    resource: repo://charts/webdav-nvr-player/templates/_helpers.tpl
  - id: openwiki-source-2dbef707347fbcbab7d5e69d
    resource: repo://charts/webdav-nvr-player/templates/deployment.yaml
  - id: openwiki-source-e530d81db235324c7345978d
    resource: repo://charts/webdav-nvr-player/templates/pvc.yaml
  - id: openwiki-source-67e2f98afdca0e1b9703b13e
    resource: repo://charts/webdav-nvr-player/values.yaml
  - id: openwiki-source-b79fbbd921df689b4bbdc82f
    resource: repo://docker-compose.yml
  - id: openwiki-source-bb1ebe868e35e9e500714501
    resource: repo://Dockerfile
generated: { by: "claude-code", at: "2026-11-08T01:57:23.266Z" }
---

# Deployment

The app ships as a single static binary inside a small Alpine image, `ghcr.io/rtan-hause/webdav-nvr-player`, with the web UI embedded. You can run it with Docker Compose or with the Helm chart in `charts/webdav-nvr-player`. Either way, the settings are environment variables (see [Configuration](../architecture/configuration.md)).

## Container image (`Dockerfile`)

The image is built in three stages:

1. **`web`** (`node:26-alpine`, on the build platform): `npm ci` and `npm run build`, which writes the Vite build into `internal/webui/dist`.
2. **`go`** (`golang:1.27-alpine`, on the build platform): cross-compiles `./cmd/nvr` with `CGO_ENABLED=0` for `$TARGETOS/$TARGETARCH`, with `-trimpath -ldflags "-s -w -X main.version=${VERSION}"`. Both the Node and Go stages run on the build machine's own architecture, so multi-arch builds don't need QEMU for the slow steps.
3. **Runtime** (`alpine`): adds `ffmpeg` (frame-grab thumbnails), `tzdata` and `ca-certificates`, and creates user and group `nvr` (uid/gid 1000) with home `/data`.

At runtime the image sets `USER nvr`, `ENV DATA_DIR=/data PORT=8080`, `VOLUME /data`, `EXPOSE 8080`, a `HEALTHCHECK` that runs `wget` against `/healthz` every 30 s, and `ENTRYPOINT ["/usr/local/bin/nvr"]`.

`make docker` builds it locally, with `VERSION` taken from `git describe`.

## Docker Compose

`docker-compose.yml` is the quick start:

```sh
cp .env.example .env    # WEBDAV_URL, WEBDAV_USERNAME, WEBDAV_PASSWORD ...
docker compose up -d
docker compose logs nvr # first start prints the admin password
```

It loads `.env` (optionally), pins `DATA_DIR=/data` and `PORT=8080` inside the container, publishes port 8080, and keeps data in the named volume `nvr-data`. `restart: unless-stopped` is set.

## Helm chart

Install from OCI:

```sh
helm install nvr oci://ghcr.io/rtan-hause/charts/webdav-nvr-player \
  --namespace nvr --create-namespace -f my-values.yaml
```

The chart is versioned separately from the app (see [CI and Releases](release-and-ci.md)). The image tag defaults to the chart's `appVersion`.

### Values → environment

| Values | Env |
|---|---|
| `webdav.url`, `webdav.root`, `webdav.username` / `webdav.password` **or** `webdav.existingSecret` (+ `usernameKey`, `passwordKey`) | `WEBDAV_*`. All of them are omitted when `webdav.url` is empty. |
| `camera.profile`, `camera.timeZone` (default `UTC`) | `CAMERA_PROFILE`, `CAMERA_TZ` |
| `indexing.webhookSecret` or `indexing.existingSecret`, `scanInterval`, `fullScanInterval`, `workers` | `INGEST_SECRET`, `SCAN_INTERVAL`, `FULL_SCAN_INTERVAL`, `INGEST_WORKERS` |
| `auth.mode`, `auth.publicURL`, `auth.oidc.*` (inline or `existingSecret`) | `AUTH`, `PUBLIC_URL`, `OIDC_*` |
| `database.url` or `database.existingSecret` | `DATABASE_URL` |
| `logLevel`, `env`, `extraEnv`, `extraEnvFrom` | anything else |

Inline secrets (WebDAV password, webhook secret, OIDC client secret, database URL) go into a Secret the chart creates. The pod template carries a `checksum/secret` annotation, so changing them restarts the pod. Each secret can instead come from an existing Secret, for example one copied in by reflector or written by Crossplane.

`nvr.validate` fails the render early when:

- `auth.mode` isn't `password`, `oidc` or `none`;
- `auth.mode` is `oidc` without an issuer, a client ID (inline or from a Secret), or `publicURL`;
- `webdav.url` is set without credentials;
- `webhookSecret` is shorter than 16 characters.

`values.schema.json` also type-checks the values.

### Workload shape

- **One replica, `Recreate` strategy.** The SQLite index lives on a ReadWriteOnce volume and the indexing queue runs in the process, so two pods must never run at the same time. This holds **even with Postgres**, because nothing coordinates dispatchers across processes. See [Store and Database Schema](../data/store-and-schema.md).
- **Probes** all hit `/healthz`: startup every 2 s × 30, liveness every 20 s, readiness every 10 s.
- **Security:** `runAsNonRoot` (uid/gid 1000), `fsGroup` 1000 with `OnRootMismatch`, seccomp `RuntimeDefault`, no privilege escalation, **read-only root filesystem**, all capabilities dropped. `/tmp` is an `emptyDir` so ffmpeg has scratch space. `automountServiceAccountToken: false`.
- **Resources:** requests 50m CPU and 128Mi memory, limit 1Gi memory. An ffmpeg frame grab from a 4K HEVC clip briefly needs a few hundred MB.

### Storage

`persistence.*` creates a PVC `<fullname>-data` (5Gi, RWO by default) mounted at `/data`, or uses `existingClaim`. It holds the SQLite database (unless `database.url` is set) and the thumbnails. Thumbnails take about 25 KB per clip, which is roughly 1 GB a month at 1,300 clips a day. They are deleted when recordings disappear from WebDAV. The PVC carries `helm.sh/resource-policy: keep`, so uninstalling the release keeps the index. With `persistence.enabled: false`, the volume is an `emptyDir` and everything is rebuilt on each restart.

### Exposure

`service` (ClusterIP:8080) plus either `ingress.*` or `httpRoute.*` (Gateway API `gateway.networking.k8s.io/v1`, with `parentRefs` and `hostnames`). Set `auth.publicURL` to the external https address so cookies are `Secure`. OIDC also requires it.

## Operating notes

- **First admin password** is logged once: `kubectl logs deploy/<release>-webdav-nvr-player | grep "created the admin user"`, or `docker compose logs nvr`.
- **Lost password:** `kubectl exec deploy/<release>-webdav-nvr-player -- nvr reset-password` (or `docker exec <container> nvr reset-password`). It prints a new password and signs that user out everywhere. See [Sign-in and Sessions](../auth/sign-in-and-sessions.md).
- **Upload webhook:** point SFTPGo at `http://<release>-webdav-nvr-player.<ns>.svc.cluster.local:8080/api/ingest`. See [Upload Webhook (SFTPGo)](../integrations/upload-webhook.md).
- **Without WebDAV configured**, the pod still becomes healthy and serves sign-in and the status page, so a misconfiguration shows up in the UI instead of as a crash loop. An invalid setting (bad URL, unknown profile, bad time zone) does crash on startup with a clear message.
- **Shutdown:** SIGTERM drains HTTP for up to 15 s, then waits for the ingester to stop.
