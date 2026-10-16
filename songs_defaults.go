package main

// Shipped default songs across updates.
//
// The default songs embedded in the binary (songs.json + songs/) carry a
// stable key ("default") and the release that first shipped them ("since").
// The number in the embedded songs.json is only used for a fresh install;
// once a scooter has its own songs.json, numbers belong to the user. At every
// service start the embedded defaults are merged in:
//
//   - a default the installation hasn't been offered yet is added under the
//     next number after the highest one in use, so a user's own songs (and
//     the unlock-melody setting, which points at a number) never move;
//   - existing entries are never renumbered, renamed or removed, and a
//     default the user removed stays removed;
//   - an improved default track file replaces the installed one only if the
//     user hasn't changed it (its checksum still matches what was installed);
//   - a new track file whose name is taken by a different file gets a new
//     name instead of overwriting it.
//
// What has been offered, and the checksum of every file installed, is kept in
// songs-defaults.json next to songs.json. songs.json itself stays the plain
// list it always was (other services read it), entries just gain an optional
// "default" key.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// legacyDefaultsSince is the release whose defaults every installation got on
// its first start; a pre-merge installation that lacks one of them had it
// removed by the user.
const legacyDefaultsSince = "1.0.0"

const defaultsStateFile = "songs-defaults.json"

// embeddedDefault is an entry of the embedded songs.json.
type embeddedDefault struct {
	Song
	Since string `json:"since"`
}

type defaultsState struct {
	// Offered maps each default key ever offered to the checksum of every
	// track file installed for it ("" when unknown, so never auto-updated).
	Offered map[string]map[string]string `json:"offered"`
}

func fileSum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func readEmbeddedDefaults(emb fs.FS) ([]embeddedDefault, error) {
	data, err := fs.ReadFile(emb, "songs.json")
	if err != nil {
		return nil, err
	}
	var defs []embeddedDefault
	if err := json.Unmarshal(data, &defs); err != nil {
		return nil, fmt.Errorf("embedded songs.json: %w", err)
	}
	return defs, nil
}

// syncDefaultSongs brings the songs.json in dir up to date with the embedded
// defaults (see the top of this file). It returns a line per change.
func syncDefaultSongs(dir string, emb fs.FS) ([]string, error) {
	defs, err := readEmbeddedDefaults(emb)
	if err != nil {
		return nil, err
	}
	songsPath := filepath.Join(dir, "songs.json")
	statePath := filepath.Join(dir, defaultsStateFile)
	trackDir := filepath.Join(dir, "songs")
	if err := os.MkdirAll(trackDir, 0755); err != nil {
		return nil, err
	}

	var catalog []Song
	fresh := false
	if data, err := os.ReadFile(songsPath); os.IsNotExist(err) {
		fresh = true
	} else if err != nil {
		return nil, err
	} else if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("songs parse error: %w", err)
	}

	state := defaultsState{Offered: map[string]map[string]string{}}
	haveState := false
	if data, err := os.ReadFile(statePath); err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, fmt.Errorf("%s: %w", defaultsStateFile, err)
		}
		if state.Offered == nil {
			state.Offered = map[string]map[string]string{}
		}
		haveState = true
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	var changes []string
	changed := false

	if !fresh && !haveState {
		changes = append(changes, migrateDefaults(catalog, defs, state, trackDir, emb)...)
		changed = true
	}

	for _, d := range defs {
		if _, offered := state.Offered[d.Default]; offered {
			continue
		}
		number := d.Number
		if !fresh || songNumberUsed(catalog, number) {
			number = nextSongNumber(catalog)
		}
		song, sums, err := installDefault(d, number, trackDir, emb)
		if err != nil {
			return changes, fmt.Errorf("default %s: %w", d.Default, err)
		}
		catalog = append(catalog, song)
		state.Offered[d.Default] = sums
		changed = true
		if !fresh {
			changes = append(changes, fmt.Sprintf("added default song %q as number %d", d.Name, number))
		}
	}

	// Improved defaults: replace files the user hasn't changed.
	for _, d := range defs {
		sums := state.Offered[d.Default]
		i := catalogIndexByDefault(catalog, d.Default)
		if sums == nil || i < 0 {
			continue
		}
		for slot, t := range d.Tracks {
			if slot >= len(catalog[i].Tracks) {
				break
			}
			want, err := fs.ReadFile(emb, path.Join("songs", t.File))
			if err != nil {
				return changes, err
			}
			disk := catalog[i].Tracks[slot].File
			installed := sums[disk]
			if installed == "" || installed == fileSum(want) {
				continue
			}
			have, err := os.ReadFile(filepath.Join(trackDir, disk))
			if err != nil || fileSum(have) != installed {
				continue // changed or removed by the user: leave it
			}
			if err := writeFileAtomic(filepath.Join(trackDir, disk), want); err != nil {
				return changes, err
			}
			sums[disk] = fileSum(want)
			changed = true
			changes = append(changes, fmt.Sprintf("updated default song %q (%s)", catalog[i].Name, disk))
		}
	}

	if !changed {
		return changes, nil
	}
	if err := writeSongsFile(songsPath, catalog); err != nil {
		return changes, err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return changes, err
	}
	if err := writeFileAtomic(statePath, append(data, '\n')); err != nil {
		return changes, err
	}
	if fresh {
		changes = append(changes, fmt.Sprintf("installed %d default songs", len(defs)))
	}
	return changes, nil
}

// migrateDefaults handles an installation from before this merge existed: it
// recognises the defaults already there by their track file names, gives them
// their key, and marks every legacy default as offered, present or not (one
// that is missing was removed by the user).
func migrateDefaults(catalog []Song, defs []embeddedDefault, state defaultsState, trackDir string, emb fs.FS) []string {
	var changes []string
	for _, d := range defs {
		legacy := d.Since == legacyDefaultsSince
		i := catalogIndexByDefault(catalog, d.Default)
		if i < 0 && legacy {
			// Only a legacy default can be on disk without its key; a newer
			// default's file name on disk belongs to one of the user's songs.
			i = catalogIndexByFiles(catalog, d.Tracks)
		}
		if i < 0 {
			if legacy {
				state.Offered[d.Default] = map[string]string{}
			}
			continue
		}
		if catalog[i].Default == "" {
			catalog[i].Default = d.Default
			changes = append(changes, fmt.Sprintf("recognised default song %q (number %d)", catalog[i].Name, catalog[i].Number))
		}
		sums := map[string]string{}
		for slot, t := range catalog[i].Tracks {
			sums[t.File] = ""
			if slot >= len(d.Tracks) {
				continue
			}
			want, err1 := fs.ReadFile(emb, path.Join("songs", d.Tracks[slot].File))
			have, err2 := os.ReadFile(filepath.Join(trackDir, t.File))
			// Only a file identical to what ships now is known to be ours;
			// anything else might be the user's and is never updated.
			if err1 == nil && err2 == nil && fileSum(have) == fileSum(want) {
				sums[t.File] = fileSum(have)
			}
		}
		state.Offered[d.Default] = sums
	}
	return changes
}

// installDefault copies a default's track files into trackDir and returns its
// catalog entry under number, with the checksum of every file installed.
func installDefault(d embeddedDefault, number int, trackDir string, emb fs.FS) (Song, map[string]string, error) {
	song := d.Song
	song.Number = number
	song.Tracks = append([]SongTrack(nil), d.Tracks...)
	sums := map[string]string{}
	for i, t := range song.Tracks {
		data, err := fs.ReadFile(emb, path.Join("songs", t.File))
		if err != nil {
			return Song{}, nil, err
		}
		name := freeTrackName(trackDir, t.File, data)
		if err := writeFileAtomic(filepath.Join(trackDir, name), data); err != nil {
			return Song{}, nil, err
		}
		song.Tracks[i].File = name
		sums[name] = fileSum(data)
	}
	return song, sums, nil
}

// freeTrackName returns name, or name with a -2, -3, ... suffix if a
// different file already has it. A file with the same content is reused.
func freeTrackName(dir, name string, data []byte) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for n := 1; ; n++ {
		candidate := name
		if n > 1 {
			candidate = fmt.Sprintf("%s-%d%s", base, n, ext)
		}
		have, err := os.ReadFile(filepath.Join(dir, candidate))
		if os.IsNotExist(err) || (err == nil && fileSum(have) == fileSum(data)) {
			return candidate
		}
	}
}

func songNumberUsed(catalog []Song, n int) bool {
	for _, s := range catalog {
		if s.Number == n {
			return true
		}
	}
	return false
}

// nextSongNumber is one after the highest number in use, never the number
// reserved for Axel F.
func nextSongNumber(catalog []Song) int {
	max := axelfReservedNumber
	for _, s := range catalog {
		if s.Number > max {
			max = s.Number
		}
	}
	return max + 1
}

func catalogIndexByDefault(catalog []Song, key string) int {
	for i, s := range catalog {
		if s.Default == key {
			return i
		}
	}
	return -1
}

func catalogIndexByFiles(catalog []Song, tracks []SongTrack) int {
	for i, s := range catalog {
		if s.Default != "" || len(s.Tracks) != len(tracks) {
			continue
		}
		same := true
		for j := range tracks {
			if s.Tracks[j].File != tracks[j].File {
				same = false
				break
			}
		}
		if same {
			return i
		}
	}
	return -1
}

// syncDefaultSongsAtStart runs the merge for the service and logs what it did.
// A failure is logged, not fatal: the existing catalog still loads.
func syncDefaultSongsAtStart(songsPath string) {
	changes, err := syncDefaultSongs(filepath.Dir(songsPath), embeddedSongs)
	for _, c := range changes {
		log.Printf("songs: %s", c)
	}
	if err != nil {
		log.Printf("songs: updating the default songs failed: %v", err)
	}
}
