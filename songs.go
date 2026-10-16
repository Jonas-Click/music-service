package main

// The song catalog: metadata (number, name, track files) describing what
// music-service can play. Defaults ship embedded in the binary (songs.json
// + songs/*.bin, go:embed'd below) and are written out to
// /data/music-service/{songs.json,songs/} on a fresh install; after that the
// files on disk are authoritative, and new or improved defaults from later
// releases are merged in without touching the user's own songs (see
// songs_defaults.go). Add your own song with `music songs add` (README.md).
//
// Every song is streamed to Tunu over CAN immediately before playing,
// except "source": "builtin-package" entries (Axel F) — Tunu plays those
// from its own vesc_tool package import and never receives an upload.
// Axel F isn't in the default catalog, but Tunu's app.lisp still hardcodes
// song number 1 as its reserved trigger for that path regardless of
// whether anything in songs.json currently uses it — so `music songs add`
// refuses that number, to avoid a custom song silently never playing
// because app.lisp intercepted it for Axel F first. Restoring Axel F as a
// default is a manual songs.json edit: add back
// {"number": 1, "name": "Axel F", "source": "builtin-package"}.

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const axelfReservedNumber = 1

//go:embed songs.json songs
var embeddedSongs embed.FS

type SongTrack struct {
	File   string  `json:"file"`
	Volume float64 `json:"volume"`
	Decay  float64 `json:"decay"`
}

type Song struct {
	Number int         `json:"number"`
	Name   string      `json:"name"`
	Source string      `json:"source,omitempty"` // "builtin-package" for Axel F; empty = streamed
	Tracks []SongTrack `json:"tracks,omitempty"`
	// Default is the key of a shipped default song (songs_defaults.go);
	// empty for the user's own songs.
	Default string `json:"default,omitempty"`
}

var (
	songCatalog []Song
	songsDir    string // directory each Song's Tracks are resolved relative to
)

// loadSongs reads the song catalog from path (a songs.json sidecar next to
// config.json), installing the embedded defaults first if path doesn't exist
// yet (a fresh install).
func loadSongs(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if _, err := syncDefaultSongs(filepath.Dir(path), embeddedSongs); err != nil {
			return fmt.Errorf("writing default song pack: %w", err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read songs %s: %w", path, err)
	}
	var catalog []Song
	if err := json.Unmarshal(data, &catalog); err != nil {
		return fmt.Errorf("songs parse error: %w", err)
	}
	for _, s := range catalog {
		if s.Number == axelfReservedNumber && s.Source != "builtin-package" {
			return fmt.Errorf("songs.json: song %q uses number %d, which is reserved for Axel F on the Tunu side (app.lisp checks it before any streamed song) — pick a different number", s.Name, axelfReservedNumber)
		}
	}

	songCatalog = catalog
	songsDir = filepath.Join(filepath.Dir(path), "songs")
	return nil
}

func songByNumber(n int) (Song, bool) {
	for _, s := range songCatalog {
		if s.Number == n {
			return s, true
		}
	}
	return Song{}, false
}

func songName(n int) string {
	if s, ok := songByNumber(n); ok {
		return s.Name
	}
	return "(unnamed)"
}

func validSong(n int) bool {
	_, ok := songByNumber(n)
	return ok
}

// sortedSongs returns the catalog ordered by number, for listing.
func sortedSongs() []Song {
	sorted := append([]Song(nil), songCatalog...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Number < sorted[j].Number })
	return sorted
}

// songTrackBytes reads a track's raw decoded-MIDI bytes from disk, resolved
// relative to songsDir.
func songTrackBytes(t SongTrack) ([]byte, error) {
	return os.ReadFile(filepath.Join(songsDir, t.File))
}

// saveSongs writes the in-memory catalog back to path — used by `music
// songs add` and `remove`.
func saveSongs(path string) error {
	return writeSongsFile(path, songCatalog)
}

// writeSongsFile writes a catalog atomically: the service rewrites songs.json
// at start when defaults change, and a power loss mid-write must not leave a
// file it can't parse.
func writeSongsFile(path string, catalog []Song) error {
	data, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'))
}
