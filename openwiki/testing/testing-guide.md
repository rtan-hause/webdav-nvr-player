---
type: guide
title: Testing Guide
description: How to run webdav-nvr-player's tests and lint, which optional dependencies (ffmpeg, Postgres, a live WebDAV server) enable extra coverage, the testing approach in each package, and where to add tests for common changes.
tags: [testing, go-test, ffmpeg, postgres, lint, typecheck]
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T01:57:23.266Z
sources:
  - id: openwiki-source-b97cfe5637e5ff53f9cd1f3b
    resource: repo://internal/auth/auth_test.go
  - id: openwiki-source-e001b7c11d255b47bdb89127
    resource: repo://internal/httpapi/api_test.go
  - id: openwiki-source-3bb260b6f594e1ed394bcfb5
    resource: repo://internal/ingest/ingest_test.go
  - id: openwiki-source-50d994847839573a455a56d1
    resource: repo://internal/media/mp4probe_test.go
  - id: openwiki-source-03556898a2d8207df157e85c
    resource: repo://internal/media/remux_test.go
  - id: openwiki-source-b736e026c56ebf14ecd78120
    resource: repo://internal/source/webdav/integration_test.go
  - id: openwiki-source-6d8c1cdec697aee752bd7c32
    resource: repo://internal/store/store_test.go
  - id: openwiki-source-012f2c78e3b1446dfc35803f
    resource: repo://Makefile
  - id: openwiki-source-14e56945b7c632a3b335dcec
    resource: repo://web/package.json
generated: { by: "claude-code", at: "2026-11-08T01:57:23.266Z" }
---

# Testing Guide

## Running

```sh
make test   # go test -race ./cmd/... ./internal/...  +  (web) npm run typecheck
make lint   # go vet, gofmt -l check, (web) eslint
```

- The Go package list is spelled out as `./cmd/... ./internal/...` and **never `./...`**, because `web/node_modules` can contain stray Go packages.
- `-race` needs cgo. The binary itself builds with `CGO_ENABLED=0`, but the tests need a C toolchain (CI installs `gcc`).
- Both targets run `npm ci` first if `web/node_modules` is out of date.
- `npm run build` in `web/` also runs the typecheck. The typecheck uses the native TypeScript compiler (`@typescript/native`).

CI runs `make lint` and `make test` with Postgres and ffmpeg available. See [CI and Releases](../operations/release-and-ci.md).

## Optional dependencies

Some tests skip themselves when a dependency is missing, so a plain `make test` on a bare machine passes with less coverage.

| Dependency | Enables | How |
|---|---|---|
| **ffmpeg** (with libx264/libx265) | Probe tests on real encoded clips; the remux checked packet for packet against `ffmpeg -movflags +faststart`; frame-grab thumbnails in the ingest and thumbs tests | On `PATH`. Tests `t.Skip` if it or the encoder is missing. |
| **Postgres** | Every store test runs a second time against Postgres | `NVR_TEST_POSTGRES_URL=postgres://postgres:test@localhost:55432/postgres?sslmode=disable`. The test **drops all app tables** first, so use a throwaway database. |
| **A live WebDAV server** | `TestLiveServer` (read-only walk of a real share) | `WEBDAV_TEST_URL`, `WEBDAV_TEST_USERNAME`, `WEBDAV_TEST_PASSWORD`, optional `WEBDAV_TEST_ROOT`; run with `-run Live -v`. |

Quick Postgres for local runs:

```sh
docker run -d --rm -p 55432:5432 -e POSTGRES_PASSWORD=test postgres:17-alpine
NVR_TEST_POSTGRES_URL=postgres://postgres:test@localhost:55432/postgres?sslmode=disable go test ./internal/store
```

## Approach by package

| Package | Approach |
|---|---|
| `internal/config` | `FromEnv` takes a `getenv` func, so tests pass maps of env vars and check defaults and validation errors. |
| `internal/store` | `backends(t, fn)` runs each test on a fresh SQLite in `t.TempDir()`, and on Postgres when configured. Covers dedupe, idempotent add and revive, the probe queue and failures, pairing, thumbnail lifecycle and deletion, range queries and stats, and re-applying migrations. See [Store and Database Schema](../data/store-and-schema.md). |
| `internal/source/webdav` | `httptest` servers that speak PROPFIND (207 XML) and Range. Checks parsing, the root folder, error mapping, `ReaderAt`, `Stat`, `Fetch` limits, and `Stream` header forwarding. |
| `internal/profile/reolink` | Table tests built from **real file names** (including ignored ones), time-zone conversion, `DateDirs` and registry lookup. New vendors should copy this. See [Camera Profiles](../ingest/camera-profiles.md). |
| `internal/media` | Encodes short clips with ffmpeg in different layouts (moov at the end, faststart, fragmented, HEVC `hvc1`), plus a synthetic Reolink-style file. Checks `Probe`, and checks that `Defragment` output matches ffmpeg's faststart remux by `framemd5` packet hashes. See [MP4 Probing and Virtual Remux](../streaming/mp4-probe-and-virtual-remux.md). |
| `internal/thumbs` | JPEG resize and path-safety checks, and ffmpeg frame grabs when available. |
| `internal/ingest` | A `fakeSource` (an in-memory file map with mtimes) plus a real SQLite store and a Reolink profile, with time injected through `Options.Now`. End-to-end: full scan → probe → pair → thumbnail; webhook with a late snapshot upgrading a frame; the settle window skipping in-progress uploads. See [Ingest Pipeline](../ingest/pipeline.md). |
| `internal/auth` | `TestMain` lowers `bcryptCost` to the minimum, because full-strength hashing takes seconds under `-race`. Covers password sign-in, sliding expiry, settings and password change, the rate limit, `none` mode, reset, `safeNext`, and a full OIDC flow against an in-process fake provider (discovery, JWKS, token). |
| `internal/httpapi` | `fakeIngest` and `fakeStream` (`httptest.ResponseRecorder` with real `Range` handling) plus a real store. Covers routes, ranges, the raw route's loopback and signature checks, webhook codes, status, fragmented-clip remux streaming, and sign-in guarding. `httpapi_test.go` covers SPA serving. |
| `internal/signedurl` | Sign/verify round trip, tampering, expiry. |
| `web/` | No unit tests. TypeScript strict typecheck and ESLint (`react-hooks`, `react-refresh`) are the gate. |

## Where to add tests for common changes

- **New API route:** `internal/httpapi/api_test.go`, using the existing fakes. If the route must work without a session, also add it to `openAPI` in `internal/auth/auth.go` and extend `TestSignInGuardsTheAPI`.
- **New store query or migration:** `store_test.go` inside `backends(...)`, so it runs on both dialects. Add the migration for both `sqlite/` and `postgres/`.
- **New camera vendor:** a table test from real names in the vendor's package.
- **Changes to probing or remux:** add a layout to the ffmpeg-encoded cases and keep the packet-for-packet comparison passing.
- **Ingest scheduling or pairing changes:** `ingest_test.go` with an injected `Now`.

## Manual checks

- `make run` builds the UI and server and starts on :8080 using `.env`. The first start logs the admin password.
- For UI work: `make dev` plus `npm run dev` in `web/` (Vite proxies `/api`).
- `#/status` in the browser shows the WebDAV connection and the index and ingest state. See [Timeline and Player UI](../player/timeline-and-ui.md).
