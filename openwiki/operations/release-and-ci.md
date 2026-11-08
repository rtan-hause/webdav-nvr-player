---
type: operations
title: CI and Releases
description: The GitHub Actions workflows that test and build webdav-nvr-player and its Helm chart, the two independent release-please setups, image and chart publishing to GHCR, and how an app release bumps the chart's appVersion to produce a chart release.
tags: [ci, release-please, github-actions, ghcr, helm, releases]
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T01:57:23.266Z
sources:
  - id: openwiki-source-c8b553d906751e787481fa78
    resource: repo://.github/workflows/chart-ci.yaml
  - id: openwiki-source-0513af278b24cfbffb756b85
    resource: repo://.github/workflows/chart-release.yaml
  - id: openwiki-source-e560413277439079b0684419
    resource: repo://.github/workflows/ci.yaml
  - id: openwiki-source-f8b7b48444f96e08372841e6
    resource: repo://.github/workflows/release.yaml
  - id: openwiki-source-14263b4e5e27713378846549
    resource: repo://release-please-config.chart.json
  - id: openwiki-source-482fa4ca84f42b04ba025fc1
    resource: repo://release-please-config.json
generated: { by: "claude-code", at: "2026-11-08T01:57:23.266Z" }
---

# CI and Releases

The app and the Helm chart are **tested and released independently**. Four workflows in `.github/workflows/` split along that line, and they all run on the self-hosted runner label `self-hosted-hause`.

| Workflow | Trigger | Does |
|---|---|---|
| `ci.yaml` | PRs and pushes to `main`, **except** chart, docs, Markdown, license, Renovate and release-bookkeeping changes | `make lint`, `make test` (with Postgres and ffmpeg), plus a Docker build with no push |
| `chart-ci.yaml` | PRs and pushes touching `charts/**` | `helm lint` + `helm template` with every file in `charts/webdav-nvr-player/ci/*.yaml` |
| `release.yaml` | pushes to `main` except chart-only changes | release-please for the app; on release, multi-arch image to GHCR |
| `chart-release.yaml` | pushes to `main` touching the chart or its release config, or manual dispatch | release-please for the chart; on release, `helm package` + `helm push` to OCI |

All workflows cancel superseded runs for PRs only. The release workflows never cancel (`cancel-in-progress: false`).

## App CI (`ci.yaml`)

The `test` job:

- runs a `postgres:18-alpine` service and sets `NVR_TEST_POSTGRES_URL`, so the store tests run against **both** SQLite and Postgres (see [Store and Database Schema](../data/store-and-schema.md));
- installs `make`, `gcc` and `libc6-dev`, because `go test -race` needs cgo even though the shipped binary doesn't;
- installs ffmpeg 8.1 from the BtbN static builds, for whichever runner architecture is in use, so thumbnail tests that shell out to ffmpeg actually run;
- sets up Go from `go.mod` and Node 26 with an npm cache;
- runs `make lint` (go vet, a gofmt check, eslint) and then `make test` (`go test -race ./cmd/... ./internal/...` plus a TypeScript typecheck).

The `docker` job builds the image with Buildx and a GitHub Actions cache, without pushing. This also checks that the frontend and binary build. See [Testing Guide](../testing/testing-guide.md).

## App releases (`release.yaml` + `release-please-config.json`)

- release-please runs in `simple` mode for the package `.` with `include-component-in-tag: false`, so app tags are plain **`vX.Y.Z`**. The current version is in `.release-please-manifest.json`.
- `exclude-paths` leaves the chart and its release files out, so chart commits never bump the app.
- `extra-files` writes the new version into `charts/webdav-nvr-player/Chart.yaml` at `$.appVersion`.
- `pull-request-title-pattern` is `fix${scope}: release${component} ${version}`. The app's release PR is titled like a `fix` commit on purpose: once merged, the `appVersion` change counts as a chart **fix**, and that produces a chart release.
- When a release is created, the `image` job checks out the tag and builds `linux/amd64,linux/arm64` with QEMU and Buildx, passing `VERSION` as a build arg (it ends up in `main.version` and `/api/version`). It pushes `ghcr.io/<repo>` tagged `X.Y.Z`, `X.Y` and `latest`.

`RELEASE_PLEASE_TOKEN`, a personal token, is used instead of `GITHUB_TOKEN` so that release PRs and tags trigger the other workflows.

## Chart releases (`chart-release.yaml` + `release-please-config.chart.json`)

- release-please runs in `helm` mode for `charts/webdav-nvr-player`, with component `webdav-nvr-player-chart` and tags **`webdav-nvr-player-chart-vX.Y.Z`**. Changelog sections: Features, Bug Fixes, Performance, Dependencies, Miscellaneous.
- Merging the chart release PR bumps `Chart.yaml`'s `version` and tags the commit. The `publish` job then lints, packages (checking that the `.tgz` name matches the version) and runs `helm push` to `oci://ghcr.io/<owner>/charts`.

### The app → chart chain

```
feat/fix commit in app code
  → app release PR "fix(main): release X.Y.Z" (bumps Chart.yaml appVersion)
  → merge: tag vX.Y.Z, GHCR image X.Y.Z
  → chart-release sees a fix touching charts/ → chart release PR
  → merge: tag webdav-nvr-player-chart-vA.B.C, OCI chart with appVersion X.Y.Z
```

The chart's `image.tag` defaults to `appVersion`, so each chart release deploys the matching app image. See [Deployment](deployment.md).

## Conventional commits

Both release-please setups read conventional commit messages (`feat:`, `fix:`, `chore:` ...). Use `feat`/`fix` for user-visible changes, and scope chart changes so they land under `charts/`. Docs-only commits don't trigger app CI at all.

## Renovate

`renovate.json` contains only the schema reference, so Renovate runs with its defaults and opens dependency update PRs for Go modules, npm, Docker base images and GitHub Actions.
