package main

// The song catalog: metadata (number, name, track files) describing what
// music-service can play. Defaults ship embedded in the binary (songs.json
// + songs/*.bin, go:embed'd below) and get written out to
// /data/music-service/{songs.json,songs/} on first run if not already
// present, so a fresh install works with no manual step. After that, the
// files on disk are authoritative — add your own song by editing
// songs.json and dropping a .bin file (raw decoded MIDI bytes) into
// songs/, which is how you add an additional song locally without ever
// needing to publish it (see README.md).
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
	"io/fs"
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
}

var (
	songCatalog []Song
	songsDir    string // directory each Song's Tracks are resolved relative to
)

// loadSongs reads the song catalog from path (a songs.json sidecar next to
// config.json), writing out the embedded defaults first if path doesn't
// exist yet — the same "apply defaults for what's missing" posture as
// loadConfig, just at the file level instead of the field level, since a
// whole song pack doesn't fit config.json's scalar-only shape.
func loadSongs(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := writeDefaultSongs(path); err != nil {
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

// writeDefaultSongs extracts the embedded default song pack (songs.json +
// songs/*.bin) next to path, for a fresh install with no customization yet.
func writeDefaultSongs(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(filepath.Join(dir, "songs"), 0755); err != nil {
		return err
	}

	data, err := embeddedSongs.ReadFile("songs.json")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}

	return fs.WalkDir(embeddedSongs, "songs", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := embeddedSongs.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, p), content, 0644)
	})
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
// songs add`. A plain write, not config.go's atomic temp-file+rename
// dance: this only ever runs interactively, once, from a terminal, so a
// power loss mid-write just means re-running the command, not a daemon
// that silently fails to start on next boot.
func saveSongs(path string) error {
	data, err := json.MarshalIndent(songCatalog, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0644)
}
