---
type: operations
title: CI and Releases
description: The GitHub Actions workflows that test and build webdav-nvr-player and its Helm chart, the two independent release-please setups, image and chart publishing to GHCR, and how an app release bumps the chart's appVersion to produce a chart release.
tags: [ci, release-please, github-actions, ghcr, helm, releases]
sources:
  - id: openwiki-source-0513af278b24cfbffb756b85
    resource: repo://.github/workflows/chart-release.yaml
  - id: openwiki-source-e560413277439079b0684419
    resource: repo://.github/workflows/ci.yaml
  - id: openwiki-source-6d4b4e707b8d60b6ccfa3425
    resource: repo://.github/workflows/openwiki-update.yml
  - id: openwiki-source-f8b7b48444f96e08372841e6
    resource: repo://.github/workflows/release.yaml
  - id: openwiki-source-14263b4e5e27713378846549
    resource: repo://release-please-config.chart.json
  - id: openwiki-source-482fa4ca84f42b04ba025fc1
    resource: repo://release-please-config.json
  - id: openwiki-source-cc2828785988d51226a4241e
    resource: repo://renovate.json
generated: { by: "claude-code", at: "2026-11-08T06:01:44.565Z" }
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T06:01:44.565Z
---

# CI and Releases

The app and the Helm chart are **released independently**, but **tested by one workflow**. All workflows run on GitHub-hosted `ubuntu-latest` runners: the repository is public, and fork pull requests must never reach self-hosted runners.

| Workflow | Trigger | Does |
|---|---|---|
| `ci.yaml` | every PR, and pushes to `main` | Go and web lint + tests, a multi-arch image build, end-to-end tests of that image, chart lint, workflow lint, then one `ci-ok` summary check |
| `release.yaml` | pushes to `main` except chart-only changes | release-please for the app; on release, multi-arch image to GHCR |
| `chart-release.yaml` | pushes to `main` touching the chart or its release config, or manual dispatch | release-please for the chart; on release, `helm package` + `helm push` to OCI |
| `openwiki-update.yml` | manual dispatch (the daily schedule is commented out) | regenerates `openwiki/` and opens a `docs: update OpenWiki` PR |

`ci.yaml` cancels superseded runs for PRs only. The release workflows never cancel (`cancel-in-progress: false`).

## CI (`ci.yaml`)

The workflow **always starts**, and its jobs skip themselves based on what changed. A `changes` job runs `dorny/paths-filter` and sets three outputs:

- `app`: anything that ends up in the image or tests it (`cmd/`, `internal/`, `web/`, `e2e/`, `go.mod`/`go.sum`, `Dockerfile`, `.dockerignore`, `Makefile`, the workflow itself);
- `chart`: `charts/**` or the workflow;
- `workflows`: any file in `.github/workflows/`.

| Job | Runs when | Does |
|---|---|---|
| `test` | `app` | Postgres 18 service with `NVR_TEST_POSTGRES_URL` set, so the store tests run against **both** SQLite and Postgres (see [Store and Database Schema](../data/store-and-schema.md)); ffmpeg 8.1 from the BtbN static builds for the runner's architecture, so thumbnail tests that shell out to ffmpeg actually run; Go from `go.mod` and Node 26; `go mod tidy -diff && go mod verify`; then `make lint` (go vet, a gofmt check, eslint) and `make test` (`go test -race`, the TypeScript typecheck and the Vitest suite). |
| `image` | `app` | Builds `linux/amd64,linux/arm64` with Buildx and a GitHub Actions cache, without pushing. Then loads a native build as `webdav-nvr-player:e2e` and runs the Playwright end-to-end tests (`e2e/run.sh` with `SKIP_BUILD=1`) against it. On failure, the Playwright report is uploaded for 14 days. |
| `chart` | `chart` | `helm lint` and `helm template` with every values file in `charts/webdav-nvr-player/ci/`. |
| `actionlint` | `workflows` | Runs a pinned `rhysd/actionlint` to catch action input renames and expression mistakes, which matters because Renovate bumps actions in release workflows that only run on `main`. |
| `ci-ok` | always | Fails if any job above failed or was cancelled; skipped jobs count as passing. |

`ci-ok` is the **only required status check** on `main`, and branch protection requires PRs to be up to date with `main` before merging. Because the workflow always runs, a PR that touches nothing testable (docs, `renovate.json`) still gets a green `ci-ok` instead of waiting forever on a check that never started. See [Testing Guide](../testing/testing-guide.md).

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

Both release-please setups read conventional commit messages (`feat:`, `fix:`, `chore:` ...). Use `feat`/`fix` for user-visible changes, and scope chart changes so they land under `charts/`. Docs-only commits still get a `ci-ok` check, but the app, image and chart jobs skip.

## Renovate

`renovate.json` extends `config:recommended`, uses semantic commit messages, and **automerges** through GitHub's auto-merge, which waits for `ci-ok`:

- Updates must be at least 3 days old (`minimumReleaseAge`); minor updates of `0.x` npm packages wait 7 days.
- **Major updates are never automerged.** They get a `major` label and need a human to read the changelog.
- The Go toolchain (`go.mod`'s `go`/`toolchain` lines and the `golang` build image) is grouped into one PR, Node versions into another, and everything under `e2e/` into an "e2e tooling" group.
- `go mod tidy` runs after Go updates, and lock files are refreshed weekly (Monday before 6 am, America/Los_Angeles).
- The `githubActionsVersions` custom manager keeps versions noted with `# renovate:` comments in workflows (for example `ACTIONLINT_VERSION`) up to date.

## OpenWiki (`openwiki-update.yml`)

The wiki under `openwiki/` is regenerated by running `openwiki code --update` with an OpenAI key and LangSmith keys from repository secrets, then opening a `docs: update OpenWiki` PR on the `openwiki/update` branch with `peter-evans/create-pull-request`. If generation fails partway, the PR keeps the pages finished before the failure. The workflow runs only on manual dispatch; its daily schedule is commented out. The wiki can also be updated locally through a coding agent.
