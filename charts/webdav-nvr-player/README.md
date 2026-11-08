# webdav-nvr-player Helm chart

Runs [webdav-nvr-player](https://github.com/rtan-hause/webdav-nvr-player): a web player for camera recordings on a WebDAV server such as SFTPGo.

```sh
helm install nvr oci://ghcr.io/rtan-hause/charts/webdav-nvr-player \
  --namespace nvr --create-namespace -f my-values.yaml
```

The chart has its own version (tags `webdav-nvr-player-chart-vX.Y.Z`), separate from the app's (`vX.Y.Z`). Each chart release runs the app version in its `appVersion` unless `image.tag` says otherwise; an app release bumps `appVersion`, which produces a new chart release.

## Example

WebDAV credentials from an existing Secret (for example one copied into the namespace by reflector), published through a Gateway API route:

```yaml
webdav:
  url: https://sftpgo.example.com/dav/
  existingSecret: sftpgo-nvr        # keys: username, password
camera:
  timeZone: America/Los_Angeles
indexing:
  webhookSecret: "<openssl rand -hex 24>"
auth:
  publicURL: https://nvr.example.com
httpRoute:
  enabled: true
  parentRefs:
    - name: my-gateway
      namespace: gateway
  hostnames: [nvr.example.com]
```

## Signing in

- `auth.mode: password` (default): one user, `admin`. Its first password is printed once in the log:
  `kubectl -n nvr logs deploy/nvr-webdav-nvr-player | grep "created the admin user"`.
  Change it under Settings. To print a new one: `kubectl -n nvr exec deploy/nvr-webdav-nvr-player -- nvr reset-password`.
- `auth.mode: oidc`: set `auth.publicURL`, `auth.oidc.issuer`, the client ID and the client secret, either inline (`auth.oidc.clientID`, `auth.oidc.clientSecret`) or from `auth.oidc.existingSecret` (keys `clientIDKey`, `clientSecretKey`; an inline `clientID` wins). Register `<publicURL>/auth/oidc/callback` as the redirect URL.
- `auth.mode: none`: no sign-in; only behind a proxy that already signs people in.

## Upload webhook

With `indexing.webhookSecret` set, point SFTPGo's Event Manager at the service so new uploads appear within seconds (rescans every 5 minutes catch anything missed):

```
POST http://<release>-webdav-nvr-player.<namespace>.svc.cluster.local:8080/api/ingest
X-NVR-Secret: <webhook secret>
{"path": "{{.VirtualPath}}"}
```

See [Upload Webhook (SFTPGo)](https://github.com/rtan-hause/webdav-nvr-player/blob/main/openwiki/integrations/upload-webhook.md).

## Storage and scaling

The pod keeps its SQLite index and thumbnails on a ReadWriteOnce volume (`persistence.*`, 5 GiB by default; thumbnails are about 1 GB a month at 1,300 clips a day). It runs as **one replica** with the Recreate strategy; the indexing queue is in-process, so more replicas aren't supported even with Postgres (`database.url`). The volume is kept when the release is uninstalled.

## Values

| Key | Default | Description |
|---|---|---|
| `image.repository` / `image.tag` | `ghcr.io/rtan-hause/webdav-nvr-player` / appVersion | Container image |
| `webdav.url` | `""` | WebDAV URL, e.g. `https://sftpgo.example.com/dav/` |
| `webdav.root` | `/` | Folder in the share where the date folders start |
| `webdav.existingSecret` | `""` | Secret with the credentials (`usernameKey`, `passwordKey`) |
| `webdav.username` / `webdav.password` | `""` | Inline credentials (the chart makes a Secret) |
| `camera.profile` | `reolink` | File and folder layout |
| `camera.timeZone` | `UTC` | Time zone in the cameras' file names |
| `indexing.webhookSecret` | `""` | Upload webhook secret (or `existingSecret` + `webhookSecretKey`); empty turns the webhook off |
| `indexing.scanInterval` / `fullScanInterval` | `5m` / `24h` | Rescan of recent folders / full crawl |
| `indexing.workers` | `2` | Clips probed at once |
| `auth.mode` | `password` | `password`, `oidc` or `none` |
| `auth.publicURL` | `""` | Address people open; required for OIDC |
| `auth.oidc.*` | | `issuer`, `clientID`/`clientSecret` or `existingSecret` + `clientIDKey`/`clientSecretKey`, `scopes`, `providerName` |
| `database.url` | `""` | Postgres URL (or `existingSecret` + `urlKey`); empty uses SQLite on the volume |
| `persistence.enabled` / `size` / `storageClass` / `existingClaim` | `true` / `5Gi` / `""` / `""` | Data volume |
| `logLevel` | `info` | `debug`, `info`, `warn`, `error` |
| `env` / `extraEnv` / `extraEnvFrom` | | Extra environment |
| `service.type` / `service.port` | `ClusterIP` / `8080` | Service |
| `ingress.*` | disabled | Ingress (`className`, `annotations`, `hosts`, `tls`) |
| `httpRoute.*` | disabled | Gateway API HTTPRoute (`parentRefs`, `hostnames`) |
| `resources` | 50m / 128Mi, limit 1Gi | ffmpeg needs headroom for 4K frame grabs |
| `podSecurityContext` / `securityContext` | non-root 1000, read-only root, no capabilities | |
| `nodeSelector` / `tolerations` / `affinity` / `priorityClassName` | | Scheduling |
