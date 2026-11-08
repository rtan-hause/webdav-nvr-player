# Files

- [MP4 Probing and Virtual Remux](mp4-probe-and-virtual-remux.md) - How webdav-nvr-player reads duration, codec and resolution from MP4 headers using small range requests, and how it serves fragmented camera uploads as a regular faststart MP4 by building a moov in memory and splicing it in front of the original bytes.
- [WebDAV Source Client](webdav-source.md) - The minimal read-only WebDAV client webdav-nvr-player uses to reach recordings, covering PROPFIND listing and stat, whole-file fetch for snapshots, the range-request ReaderAt used for probing and remuxing, pass-through streaming, error types, and how credentials are handled.
