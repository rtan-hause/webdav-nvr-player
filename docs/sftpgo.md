# Indexing uploads right away (SFTPGo webhook)

Without a webhook, the player finds new recordings by rescanning today's and yesterday's folders every `SCAN_INTERVAL` (5 minutes by default). With one, SFTPGo tells the player about each file as soon as its upload finishes, so new clips show up within seconds. The rescans keep running either way and catch anything the webhook missed.

## 1. Turn the webhook on in the player

Set a shared secret of at least 16 characters:

```sh
openssl rand -hex 24
```

```env
INGEST_SECRET=<that value>
```

The player now accepts `POST /api/ingest` with the header `X-NVR-Secret: <secret>` and a JSON body naming the file relative to `WEBDAV_ROOT`:

```json
{"path": "2026/11/06/Front of House_00_20261106215852.mp4"}
```

It answers `202` for a clip or snapshot, `204` for files the camera profile ignores (such as Reolink's `.txt` upload test), `404` if the file isn't on the server, and `401` for a wrong secret. A leading `/` is fine.

Test it by hand:

```sh
curl -i -X POST http://localhost:8088/api/ingest \
  -H "X-NVR-Secret: $INGEST_SECRET" -H 'Content-Type: application/json' \
  -d '{"path": "2026/11/06/Living Room_00_20261106215852.mp4"}'
```

## 2. Add an SFTPGo event rule

In SFTPGo's WebAdmin, under **Event Manager**:

1. **Actions → Add**: type **HTTP**.
   - Endpoint: the player's `/api/ingest` URL, for example `http://webdav-nvr-player.nvr.svc.cluster.local:8080/api/ingest`. It must be reachable from the SFTPGo pod.
   - Method: `POST`
   - Header: `X-NVR-Secret` = your secret
   - Body: `{"path": "{{.VirtualPath}}"}`
   - Timeout: 10 seconds
2. **Rules → Add**: trigger **Filesystem events**, event **Upload**, limited to the camera's upload user (for example `reolink`), running the action above.

`{{.VirtualPath}}` is the path as the uploading user sees it. The paths line up when the uploading user and the player's WebDAV user see the same folder at their roots, as with the `reolink` and `nvr` users sharing the `reolink-recordings` folder at `/`. If the player's user sees the recordings deeper in its tree, set `WEBDAV_ROOT` to that folder.

### Terraform (sftpgo provider)

The same thing with the [`drakkan/sftpgo`](https://registry.terraform.io/providers/drakkan/sftpgo) provider, in the style of `infra-terraform/sftpgo`. Field names follow provider 0.0.x; run `terraform validate` against your version.

```hcl
resource "sftpgo_action" "nvr_ingest" {
  name        = "nvr-ingest"
  description = "Tell webdav-nvr-player about each finished camera upload."
  type        = 1 # HTTP

  options = {
    http_config = {
      endpoint = "http://webdav-nvr-player.nvr.svc.cluster.local:8080/api/ingest"
      method   = "POST"
      timeout  = 10
      headers = [{
        key   = "X-NVR-Secret"
        value = data.kubernetes_secret_v1.nvr_ingest.data["secret"]
      }]
      # SFTPGo 2.7.6 substitutes {{.Name}}-style placeholders (JSON-escaped).
      body = jsonencode({ path = "{{.VirtualPath}}" })
    }
  }
}

resource "sftpgo_rule" "nvr_ingest" {
  name        = "nvr-ingest"
  description = "Index Reolink uploads as soon as they finish."
  status      = 1
  trigger     = 1 # Filesystem events

  conditions = {
    fs_events = ["upload"]
    options = {
      names = [{ pattern = "reolink" }] # only the camera upload user
    }
  }

  actions = [{
    name = sftpgo_action.nvr_ingest.name
  }]
}
```

Keep the secret in a Kubernetes Secret, generated the same way as `sftpgo-nvr` (an External Secrets `Password` generator), and read it from both Terraform and the player's deployment.

## What happens to each upload

1. The webhook (or a rescan) adds the file to the index.
2. A clip is probed for duration, codec and resolution by reading only its MP4 headers. Reolink writes fragmented MP4s, so this means about one small range request per 2-second fragment, with no full download.
3. The camera's snapshot becomes the thumbnail. Reolink's snapshot names can be a few seconds off from the clip's, so they're matched by time, not by name. If none arrives within 60 seconds of the upload, ffmpeg grabs a frame from the clip instead.
4. When a file disappears from the server (retention), the next scan drops it from the index and deletes its thumbnail.
