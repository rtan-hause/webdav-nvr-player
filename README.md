# webdav-nvr-player

A web player for security-camera recordings stored on a WebDAV server. Cameras upload clips over FTP/SFTP (for example to [SFTPGo](https://github.com/drakkan/sftpgo)), and this app indexes them and plays them back on a scrubbable multi-camera timeline. The WebDAV account it uses only needs read access.

**Status:** early development. Indexing, thumbnails, streaming and the player (Split layout with a day timeline, theater mode, phone layout) work; sign-in is next. `make run` starts everything. See [docs/PLAN.md](docs/PLAN.md) for the design and [docs/mockups/index.html](docs/mockups/index.html) for the target UI.

Planned highlights:

- Continuous day timeline with one lane per camera, zoom, and drag-to-scrub across clips
- HEVC and H.264 passthrough (no transcoding)
- Thumbnails from the camera's own snapshots, with an ffmpeg fallback
- New clips indexed as soon as the upload finishes (webhook), with periodic rescans as a safety net
- SQLite by default, Postgres optional
- Camera support through per-vendor profiles (Reolink first)

## Development

Requires Go 1.26+, Node 26+, and ffmpeg.

Settings come from `.env`. The first `make run` creates it from [.env.example](.env.example) and stops so you can fill in your WebDAV URL, username and password.

```sh
make run        # build the UI and server, start on :8080 with .env
make test       # Go tests + frontend typecheck
make lint       # go vet, gofmt, eslint
make docker     # build the container image
```

For frontend work, run `make dev` (Go server) and `npm run dev` in `web/` (Vite with hot reload; it proxies `/api` to :8080).

## License

[MIT](LICENSE)
