// Package profile describes how a camera vendor names and lays out its uploads.
//
// Each vendor is a Go package that implements Profile and calls Register from
// init. cmd/nvr imports every vendor package for its side effect.
package profile

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Kind says what a file is.
type Kind int

const (
	Ignore   Kind = iota // not a recording (upload tests, sidecars, unknown files)
	Clip                 // a video recording
	Snapshot             // a still image the camera uploads alongside clips
)

func (k Kind) String() string {
	switch k {
	case Clip:
		return "clip"
	case Snapshot:
		return "snapshot"
	default:
		return "ignore"
	}
}

// Media is what a profile learns from a file's path alone.
type Media struct {
	Kind    Kind
	Camera  string // display name, e.g. "Front of House"
	Channel string // vendor channel id, "" if the vendor has none
	// Local is the camera's wall-clock time encoded in the name. Only the
	// date and clock fields are meaningful; the caller applies the camera's
	// time zone with In.
	Local time.Time
}

// In converts the wall-clock time to an instant in loc.
func (m Media) In(loc *time.Location) time.Time {
	l := m.Local
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), l.Nanosecond(), loc)
}

// Pairing says which uploaded snapshot belongs to which clip.
type Pairing struct {
	// A snapshot pairs with a clip of the same camera when it was taken between
	// Before ahead of the clip's start and the clip's end. The closest to the
	// start wins, and each snapshot pairs with at most one clip.
	Before time.Duration
	// Wait is how long a clip waits for its snapshot to arrive before a frame
	// is extracted from the video instead.
	Wait time.Duration
}

// Profile is one camera vendor's naming and layout conventions.
type Profile interface {
	ID() string   // stable id used in config, e.g. "reolink"
	Name() string // human name, e.g. "Reolink (FTP upload)"
	// Classify inspects a path relative to the recordings root ("/"-separated).
	Classify(relPath string) Media
	// DateDirs lists the folders (relative to the root) that hold files for a
	// camera-local calendar day, so routine rescans only list what they need.
	DateDirs(year int, month time.Month, day int) []string
	Pairing() Pairing
}

var (
	mu       sync.RWMutex
	registry = map[string]Profile{}
)

// Register adds a profile. It panics on a duplicate id, which is a programming error.
func Register(p Profile) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[p.ID()]; dup {
		panic("profile: duplicate id " + p.ID())
	}
	registry[p.ID()] = p
}

// Lookup returns the profile with the given id.
func Lookup(id string) (Profile, error) {
	mu.RLock()
	defer mu.RUnlock()
	if p, ok := registry[id]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("unknown camera profile %q (supported: %v)", id, idsLocked())
}

// IDs lists registered profile ids in order.
func IDs() []string {
	mu.RLock()
	defer mu.RUnlock()
	return idsLocked()
}

func idsLocked() []string {
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
