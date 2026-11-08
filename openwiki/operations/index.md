# Files

- [Deployment](deployment.md) - How to run webdav-nvr-player in production, covering the multi-arch container image, the docker-compose example, the Helm chart (single replica, Recreate strategy, PVC, Secrets, Ingress or HTTPRoute, probes, hardened security context), the data volume, and admin password recovery in a container.
- [CI and Releases](release-and-ci.md) - The GitHub Actions workflows that test and build webdav-nvr-player and its Helm chart, the two independent release-please setups, image and chart publishing to GHCR, and how an app release bumps the chart's appVersion to produce a chart release.
