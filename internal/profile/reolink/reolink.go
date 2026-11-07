// Package reolink is the profile for Reolink cameras uploading over FTP/SFTP.
//
// Reolink names files <Camera>_<CH>_<YYYYMMDDHHMMSS>.<ext> in the camera's
// local time, for example "Front of House_00_20261106215852.mp4", and (with
// dated subdirectories enabled) stores them under YYYY/MM/DD/. Recordings are
// split into ~28 s files. A JPEG snapshot is usually uploaded with each clip,
// often with a name a second or more off from the clip's. A one-off
// "<Camera>_00_<ts>.txt" file is written when the FTP settings are tested.
package reolink

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/rtan-hause/webdav-nvr-player/internal/profile"
)

func init() { profile.Register(Profile{}) }

// Profile implements profile.Profile for Reolink.
type Profile struct{}

var name = regexp.MustCompile(`^(.+)_(\d{2})_(\d{14})\.([A-Za-z0-9]+)$`)

func (Profile) ID() string   { return "reolink" }
func (Profile) Name() string { return "Reolink (FTP upload)" }

// Classify reads the camera, channel and time from the file name. Folders are
// not consulted, so both YYYY/MM/DD/ and older Camera/YYYY/MM/DD/ layouts work.
func (Profile) Classify(relPath string) profile.Media {
	m := name.FindStringSubmatch(path.Base(relPath))
	if m == nil {
		return profile.Media{}
	}
	var kind profile.Kind
	switch strings.ToLower(m[4]) {
	case "mp4":
		kind = profile.Clip
	case "jpg", "jpeg":
		kind = profile.Snapshot
	default:
		return profile.Media{} // .txt upload tests and anything else
	}
	local, err := time.Parse("20060102150405", m[3])
	if err != nil {
		return profile.Media{} // e.g. month 13
	}
	camera := strings.TrimSpace(m[1])
	if camera == "" {
		return profile.Media{}
	}
	return profile.Media{Kind: kind, Camera: camera, Channel: m[2], Local: local}
}

func (Profile) DateDirs(year int, month time.Month, day int) []string {
	return []string{fmt.Sprintf("%04d/%02d/%02d", year, int(month), day)}
}

// Pairing: measured on 323k clips, 61% of snapshots are stamped 0–1 s after
// the clip's name and a few percent up to 4 s after; anything inside the clip
// is a valid thumbnail. Snapshots normally arrive before the (larger) clip.
func (Profile) Pairing() profile.Pairing {
	return profile.Pairing{Before: 2 * time.Second, Wait: 60 * time.Second}
}
