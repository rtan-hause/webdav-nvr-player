---
type: integration
title: Upload Webhook (SFTPGo)
description: How an upload server such as SFTPGo tells webdav-nvr-player about finished uploads through POST /api/ingest, covering the shared secret, the request and response contract, path alignment with WEBDAV_ROOT, the rescan endpoint, and the reference SFTPGo Event Manager and Terraform setup.
tags: [webhook, sftpgo, ingest, integration, terraform]
verified:
  - by: openwiki/0.7.2
    at: 2026-11-08T01:57:23.266Z
sources:
  - id: openwiki-source-5141266584974a63e46300c9
    resource: repo://docs/sftpgo.md
  - id: openwiki-source-a8910515ddd14810ad43f5c1
    resource: repo://internal/config/config.go
  - id: openwiki-source-7aaec0c52e398dfd7e502e0b
    resource: repo://internal/httpapi/api.go
  - id: openwiki-source-37047f321b6d15c60cf15860
    resource: repo://internal/ingest/ingest.go
generated: { by: "claude-code", at: "2026-11-08T01:57:23.266Z" }
---

# Upload Webhook (SFTPGo)

Without a webhook, new recordings show up when the next recent scan runs (every `SCAN_INTERVAL`, 5 min by default). With one, the upload server notifies the player as soon as each upload finishes, and new clips appear within seconds. **Rescans keep running either way** and catch anything the webhook missed. See [Ingest Pipeline](../ingest/pipeline.md).

The step-by-step guide is in `docs/sftpgo.md`. This page summarizes the contract and what happens on the player's side.

## Enabling it

Set `INGEST_SECRET` to at least 16 characters (`openssl rand -hex 24`). Startup rejects shorter values. While it's empty, both `/api/ingest` and `/api/rescan` answer **403** ("the upload webhook is off"). The status page reports whether the webhook is enabled (`webhook` in `/api/status`).

These endpoints are exempt from session sign-in. Their protection is the `X-NVR-Secret` header, compared in constant time. See [Sign-in and Sessions](../auth/sign-in-and-sessions.md).

## Request contract

```http
POST /api/ingest
X-NVR-Secret: <INGEST_SECRET>
Content-Type: application/json

{"path": "2026/11/06/Front of House_00_20261106215852.mp4"}
```

- `path` is **relative to `WEBDAV_ROOT`**. A leading `/` is fine: the ingester normalizes it with `path.Clean`, which also stops `..` from escaping the root.
- At most 64 KiB of body is read. A missing or empty `path` gets **400** with an example body.

| Response | Meaning |
|---|---|
| `202 {"kind":"clip"}` / `{"kind":"snapshot"}` | Indexed. Processing (probe, pairing, thumbnail) starts right away. |
| `204` | The camera profile ignores this file (for example Reolink's `.txt` FTP test). |
| `401` | Missing or wrong secret. |
| `403` | Webhook disabled (no `INGEST_SECRET`). |
| `404` | The file isn't on the WebDAV server. Usually `WEBDAV_ROOT` doesn't match the upload server's virtual path. |
| `502` | Another error talking to WebDAV or the database. |

The handler stats only that one file. It does not list the folder, which can hold thousands of files on a busy day. Sending the same path twice is harmless, because inserts are idempotent.

`POST /api/rescan` (same header, no body) starts a full crawl in the background. It answers **202** `{"status":"started"}`, or **409** if a scan is already running. It's useful after restoring files or changing retention.

Manual test:

```sh
curl -i -X POST http://localhost:8080/api/ingest \
  -H "X-NVR-Secret: $INGEST_SECRET" -H 'Content-Type: application/json' \
  -d '{"path": "2026/11/06/Living Room_00_20261106215852.mp4"}'
```

## SFTPGo configuration

In SFTPGo's WebAdmin, under **Event Manager**:

1. **Action**, type HTTP: `POST` to the player's `/api/ingest` URL (for example the in-cluster Service `http://webdav-nvr-player.nvr.svc.cluster.local:8080/api/ingest`), header `X-NVR-Secret`, body `{"path": "{{.VirtualPath}}"}`, timeout 10 s.
2. **Rule**: trigger *Filesystem events*, event *Upload*, limited to the camera's upload user, running that action.

`docs/sftpgo.md` also has an equivalent Terraform definition using the `drakkan/sftpgo` provider (`sftpgo_action` and `sftpgo_rule`), with the secret read from a Kubernetes Secret that the player's deployment also uses.

### Path alignment

`{{.VirtualPath}}` is the path as the *uploading* user sees it. It only matches what the player expects if the uploading user and the player's WebDAV user see the recordings folder at the same root. In the reference setup, both a `reolink` and an `nvr` user have the `reolink-recordings` folder mounted at `/`. If the player's user sees the recordings deeper in its tree, set `WEBDAV_ROOT` to that folder. A persistent **404** from the webhook almost always means this alignment is wrong.

## What happens after a 202

1. The row is inserted as `pending`, and the dispatcher wakes.
2. The clip is probed by reading only its MP4 headers. For a fragmented Reolink file that is about one small range request per fragment, with no full download. See [MP4 Probing and Virtual Remux](../streaming/mp4-probe-and-virtual-remux.md).
3. The camera's snapshot is paired by time. If none arrives within the profile's wait (60 s for Reolink, measured from the upload time), ffmpeg grabs a frame instead. See [Snapshot Pairing and Thumbnails](../ingest/snapshot-pairing-and-thumbnails.md).
4. When retention removes the file later, the next scan marks it deleted and removes its thumbnail.

## Deployment notes

The upload server must be able to reach the player's HTTP port. In Kubernetes that is usually the chart's Service (see [Deployment](../operations/deployment.md)). Keep `INGEST_SECRET` in a Secret.
