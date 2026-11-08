# Files

- [Camera Profiles](camera-profiles.md) - The profile.Profile interface that teaches webdav-nvr-player a camera vendor's file names, folder layout and snapshot timing, covering the Reolink reference profile, how profiles are registered and selected, and how to add a new vendor.
- [Ingest Pipeline](pipeline.md) - How webdav-nvr-player keeps its index in step with the WebDAV server, covering the upload webhook, recent and full scans, the settle window, deletion reconciliation, and the dispatcher and worker pool that use database rows as the work queue, with retry backoff and scan status.
- [Snapshot Pairing and Thumbnails](snapshot-pairing-and-thumbnails.md) - How webdav-nvr-player matches camera JPEG snapshots to clips by time window, waits for late snapshots, falls back to an ffmpeg frame grab over a signed loopback URL, and stores and serves 480 px thumbnails.
