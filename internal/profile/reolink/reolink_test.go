package reolink

import (
	"testing"
	"time"

	"github.com/rtan-hause/webdav-nvr-player/internal/profile"
)

func TestClassify(t *testing.T) {
	p := Profile{}
	for _, tc := range []struct {
		path    string
		kind    profile.Kind
		camera  string
		channel string
		local   string
	}{
		// Real names from the SFTPGo share and the old vsftpd archive.
		{"2026/11/06/Living Room_00_20261106215852.mp4", profile.Clip, "Living Room", "00", "2026-11-06 21:58:52"},
		{"2026/11/06/Living Room_00_20261106215851.jpg", profile.Snapshot, "Living Room", "00", "2026-11-06 21:58:51"},
		{"reolink/2026/06/04/Front of House_00_20260604071502.mp4", profile.Clip, "Front of House", "00", "2026-06-04 07:15:02"},
		{"Garage_01_20260101000000.MP4", profile.Clip, "Garage", "01", "2026-01-01 00:00:00"},
		{"Backyard_00_20261231235959.jpeg", profile.Snapshot, "Backyard", "00", "2026-12-31 23:59:59"},
		// Camera names can contain underscores.
		{"Side_Gate_00_20261106101010.mp4", profile.Clip, "Side_Gate", "00", "2026-11-06 10:10:10"},

		// Ignored.
		{"2026/11/06/Living Room_00_20261106215354.txt", profile.Ignore, "", "", ""},
		{"2026/11/06/Living Room_00_20261306215354.mp4", profile.Ignore, "", "", ""}, // month 13
		{"2026/11/06/notes.mp4", profile.Ignore, "", "", ""},
		{"2026/11/06/Living Room_0_20261106215354.mp4", profile.Ignore, "", "", ""},
		{"2026/11/06/_00_20261106215354.mp4", profile.Ignore, "", "", ""},
		{"2026/11/06/Living Room_00_20261106215354.mp4.tmp", profile.Ignore, "", "", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			m := p.Classify(tc.path)
			if m.Kind != tc.kind {
				t.Fatalf("kind = %v, want %v", m.Kind, tc.kind)
			}
			if tc.kind == profile.Ignore {
				return
			}
			if m.Camera != tc.camera || m.Channel != tc.channel || m.Local.Format("2006-01-02 15:04:05") != tc.local {
				t.Fatalf("got camera=%q channel=%q local=%s", m.Camera, m.Channel, m.Local)
			}
		})
	}
}

func TestMediaInTimeZone(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	m := Profile{}.Classify("Living Room_00_20261106215852.mp4")
	got := m.In(la).UTC().Format(time.RFC3339)
	if got != "2026-11-07T05:58:52Z" { // PST is UTC-8 after Nov 1
		t.Fatalf("UTC = %s", got)
	}
}

func TestDateDirsAndRegistry(t *testing.T) {
	if got := (Profile{}).DateDirs(2026, time.November, 6); len(got) != 1 || got[0] != "2026/11/06" {
		t.Fatalf("DateDirs = %v", got)
	}
	p, err := profile.Lookup("reolink")
	if err != nil || p.ID() != "reolink" {
		t.Fatalf("registry lookup: %v %v", p, err)
	}
	if _, err := profile.Lookup("hikvision"); err == nil {
		t.Fatal("unknown profile should error")
	}
}
