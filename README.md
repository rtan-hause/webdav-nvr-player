# webdav-nvr-player

A web player for security-camera recordings stored on a WebDAV server. Cameras upload clips over FTP/SFTP (for example to [SFTPGo](https://github.com/drakkan/sftpgo)), and this app indexes them and plays them back on a scrubbable multi-camera timeline. The WebDAV account it uses only needs read access.

**Status:** early development, in daily use with Reolink cameras.

- Continuous day timeline with one lane per camera, zoom, drag-to-pan, and a playhead that scrubs across clips
- HEVC and H.264 passthrough (no transcoding); fragmented MP4 uploads are served as regular MP4 on the fly so browsers can seek
- Thumbnails from the camera's own snapshots, with an ffmpeg fallback
- New clips indexed as soon as the upload finishes (webhook), with periodic rescans as a safety net
- SQLite by default, Postgres optional
- Theater mode and a phone layout
- Sign-in with a local admin password or OpenID Connect
- Camera support through per-vendor profiles (Reolink first)

## Quick start (Docker Compose)

```sh
cp .env.example .env    # fill in WEBDAV_URL, WEBDAV_USERNAME, WEBDAV_PASSWORD
docker compose up -d
docker compose logs nvr # the first start prints the admin password
```

Then open http://localhost:8080. [.env.example](.env.example) documents every setting.

To index uploads the moment they finish instead of waiting for the next rescan, set `INGEST_SECRET` and point your upload server's webhook at the player: see [docs/sftpgo.md](docs/sftpgo.md).

### Kubernetes

A Helm chart is in [charts/webdav-nvr-player](charts/webdav-nvr-player), published to `oci://ghcr.io/rtan-hause/charts/webdav-nvr-player`. It's versioned and released separately from the app.

## Signing in

`AUTH` in `.env` picks how people sign in:

- `password` (default): one user, `admin`. On first start the server makes a random password and prints it in its log (`make run` shows it in the terminal). Change it under Settings. If it's lost, `make reset-password` (or `nvr reset-password` inside the container) prints a new one.
- `oidc`: sign in through an OpenID Connect provider such as Zitadel, Authentik or Keycloak. Set `PUBLIC_URL`, `OIDC_ISSUER`, `OIDC_CLIENT_ID` and `OIDC_CLIENT_SECRET`, and register `PUBLIC_URL/auth/oidc/callback` as the redirect URL.
- `none`: no sign-in, for running behind a proxy that already signs people in.

Everyone who signs in can do everything. How long an unused device stays signed in (30 days by default) is set under Settings.

## Development

Requires Go 1.27+, Node 26+, and ffmpeg.

Settings come from `.env`. The first `make run` creates it from [.env.example](.env.example) and stops so you can fill in your WebDAV URL, username and password.

```sh
make run        # build the UI and server, start on :8080 with .env
make test       # Go tests + frontend typecheck
make lint       # go vet, gofmt, eslint
make docker     # build the container image
```

For frontend work, run `make dev` (Go server) and `npm run dev` in `web/` (Vite with hot reload; it proxies `/api` to :8080).

### Adding a camera vendor

Each vendor is a Go package under [internal/profile](internal/profile) that implements `profile.Profile` ([internal/profile/profile.go](internal/profile/profile.go)):

- `ID` / `Name`: the value people put in `CAMERA_PROFILE`, and a display name.
- `Classify`: turn a path relative to `WEBDAV_ROOT` into a clip, a snapshot or an ignored file, with the camera name, channel and the local time from the file name.
- `DateDirs`: the folders that hold one calendar day, so routine rescans only list those.
- `Pairing`: how far before a clip's start its snapshot may be stamped, and how long to wait for it before grabbing a frame with ffmpeg.

Register it from the package's `init` with `profile.Register`, add a blank import to [cmd/nvr/main.go](cmd/nvr/main.go), and add table tests built from real file names. [internal/profile/reolink](internal/profile/reolink) is the reference.

## Documentation

- [docs/design-decisions.md](docs/design-decisions.md): why the player works the way it does, and its known limits.
- [docs/sftpgo.md](docs/sftpgo.md): wiring SFTPGo's upload webhook to the player.
- [openwiki/](openwiki/): the generated code wiki, covering architecture, ingest, streaming, the player and operations. Start at its [Quickstart](openwiki/quickstart.md).

The wiki is written and kept current by [OpenWiki](https://github.com/langchain-ai/openwiki), with every fact backed by source evidence under `openwiki/.claims/`. Don't hand-edit generated pages; change the code or the docs above and let OpenWiki regenerate:

- **Via your coding agent:** ask it to *"Update this repository's OpenWiki for changes since its last successful run."*
- **Via CLI:** `openwiki code --update`

## License

[MIT](LICENSE)
