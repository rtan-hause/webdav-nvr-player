# webdav-nvr-player

A web player for security-camera recordings stored on a WebDAV server. Cameras upload clips over FTP/SFTP (for example to [SFTPGo](https://github.com/drakkan/sftpgo)), and this app indexes them and plays them back on a scrubbable multi-camera timeline. The WebDAV account it uses only needs read access.

**Status:** early development. Indexing, thumbnails, streaming and the player (Split layout with a day timeline, theater mode, phone layout) work, with sign-in (a local admin password or OpenID Connect). `make run` starts everything. See [docs/PLAN.md](docs/PLAN.md) for the design and [docs/mockups/index.html](docs/mockups/index.html) for the target UI.

Planned highlights:

- Continuous day timeline with one lane per camera, zoom, and drag-to-scrub across clips
- HEVC and H.264 passthrough (no transcoding)
- Thumbnails from the camera's own snapshots, with an ffmpeg fallback
- New clips indexed as soon as the upload finishes (webhook), with periodic rescans as a safety net
- SQLite by default, Postgres optional
- Camera support through per-vendor profiles (Reolink first)

## Development

Requires Go 1.27+, Node 26+, and ffmpeg.

Settings come from `.env`. The first `make run` creates it from [.env.example](.env.example) and stops so you can fill in your WebDAV URL, username and password.

```sh
make run        # build the UI and server, start on :8080 with .env
make test       # Go tests + frontend typecheck
make lint       # go vet, gofmt, eslint
make docker     # build the container image
```

### Kubernetes

A Helm chart is in [charts/webdav-nvr-player](charts/webdav-nvr-player), published to `oci://ghcr.io/rtan-hause/charts/webdav-nvr-player`. It's versioned and released separately from the app.

### Signing in

`AUTH` in `.env` picks how people sign in:

- `password` (default): one user, `admin`. On first start the server makes a random password and prints it in its log (`make run` shows it in the terminal). Change it under Settings. If it's lost, `make reset-password` (or `nvr reset-password` inside the container) prints a new one.
- `oidc`: sign in through an OpenID Connect provider such as Zitadel, Authentik or Keycloak. Set `PUBLIC_URL`, `OIDC_ISSUER`, `OIDC_CLIENT_ID` and `OIDC_CLIENT_SECRET`, and register `PUBLIC_URL/auth/oidc/callback` as the redirect URL.
- `none`: no sign-in, for running behind a proxy that already signs people in.

Everyone who signs in can do everything. How long an unused device stays signed in (30 days by default) is set under Settings.

For frontend work, run `make dev` (Go server) and `npm run dev` in `web/` (Vite with hot reload; it proxies `/api` to :8080).

## License

[MIT](LICENSE)
