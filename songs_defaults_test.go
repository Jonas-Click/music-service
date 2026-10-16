package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// shipped builds an embedded default pack: songs.json plus track files.
func shipped(t *testing.T, defs []embeddedDefault, files map[string]string) fstest.MapFS {
	t.Helper()
	data, err := json.Marshal(defs)
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{"songs.json": {Data: data}}
	for name, content := range files {
		fsys["songs/"+name] = &fstest.MapFile{Data: []byte(content)}
	}
	return fsys
}

func def(key string, number int, since, name string, files ...string) embeddedDefault {
	d := embeddedDefault{Song: Song{Number: number, Name: name, Default: key}, Since: since}
	for _, f := range files {
		d.Tracks = append(d.Tracks, SongTrack{File: f, Volume: 1.5, Decay: 0.92})
	}
	return d
}

var v100Defaults = []embeddedDefault{
	def("cucaracha", 2, "1.0.0", "La Cucaracha", "cucaracha-mel.bin"),
	def("korobeiniki", 3, "1.0.0", "Korobeiniki", "korobeiniki-mel.bin", "korobeiniki-bass.bin"),
	def("bday", 4, "1.0.0", "Happy Birthday to You", "bday-mel.bin"),
}

var v100Files = map[string]string{
	"cucaracha-mel.bin":    "CUCA",
	"korobeiniki-mel.bin":  "KORO-MEL",
	"korobeiniki-bass.bin": "KORO-BASS",
	"bday-mel.bin":         "BDAY",
}

func readCatalog(t *testing.T, dir string) []Song {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "songs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c []Song
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func numbers(c []Song) map[int]string {
	m := map[int]string{}
	for _, s := range c {
		m[s.Number] = s.Name
	}
	return m
}

func runSync(t *testing.T, dir string, emb fstest.MapFS) []string {
	t.Helper()
	changes, err := syncDefaultSongs(dir, emb)
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

// writeV100Install lays out what v1.0.0 wrote on first start (no keys, no
// state file), optionally without some defaults and with extra user songs.
func writeV100Install(t *testing.T, dir string, skip map[string]bool, extra []Song, extraFiles map[string]string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "songs"), 0755)
	var c []Song
	for _, d := range v100Defaults {
		if skip[d.Default] {
			continue
		}
		s := d.Song
		s.Default = ""
		c = append(c, s)
		for _, tr := range d.Tracks {
			os.WriteFile(filepath.Join(dir, "songs", tr.File), []byte(v100Files[tr.File]), 0644)
		}
	}
	c = append(c, extra...)
	for name, content := range extraFiles {
		os.WriteFile(filepath.Join(dir, "songs", name), []byte(content), 0644)
	}
	data, _ := json.Marshal(c)
	os.WriteFile(filepath.Join(dir, "songs.json"), data, 0644)
}

func TestFreshInstall(t *testing.T) {
	dir := t.TempDir()
	runSync(t, dir, shipped(t, v100Defaults, v100Files))
	got := numbers(readCatalog(t, dir))
	if got[2] != "La Cucaracha" || got[3] != "Korobeiniki" || got[4] != "Happy Birthday to You" || len(got) != 3 {
		t.Fatalf("fresh install: %v", got)
	}
	if changes := runSync(t, dir, shipped(t, v100Defaults, v100Files)); len(changes) != 0 {
		t.Fatalf("second run changed something: %v", changes)
	}
}

func TestUpgradeKeepsUserSongsAndAddsNewDefaultsAfterThem(t *testing.T) {
	dir := t.TempDir()
	user := []Song{
		{Number: 5, Name: "Last Christmas", Tracks: []SongTrack{{File: "lastxmas-mel.bin"}}},
		{Number: 6, Name: "The Final Countdown", Tracks: []SongTrack{{File: "fc-mel.bin"}}},
	}
	writeV100Install(t, dir, nil, user, map[string]string{"lastxmas-mel.bin": "LX", "fc-mel.bin": "FC"})

	// First start of the new release: same defaults, nothing new.
	runSync(t, dir, shipped(t, v100Defaults, v100Files))
	c := readCatalog(t, dir)
	if got := numbers(c); len(got) != 5 || got[5] != "Last Christmas" || got[6] != "The Final Countdown" {
		t.Fatalf("after migration: %v", got)
	}
	if c[catalogIndexByDefault(c, "korobeiniki")].Number != 3 {
		t.Fatal("Korobeiniki was not recognised as a default")
	}

	// A later release ships two new defaults.
	defs := append(append([]embeddedDefault{}, v100Defaults...),
		def("jinglebells", 5, "1.2.0", "Jingle Bells", "jingle-mel.bin"),
		def("ode", 6, "1.2.0", "Ode to Joy", "ode-mel.bin"))
	files := map[string]string{"jingle-mel.bin": "JINGLE", "ode-mel.bin": "ODE"}
	for k, v := range v100Files {
		files[k] = v
	}
	changes := runSync(t, dir, shipped(t, defs, files))
	got := numbers(readCatalog(t, dir))
	if got[5] != "Last Christmas" || got[6] != "The Final Countdown" || got[7] != "Jingle Bells" || got[8] != "Ode to Joy" {
		t.Fatalf("new defaults: %v (changes %v)", got, changes)
	}
	if changes := runSync(t, dir, shipped(t, defs, files)); len(changes) != 0 {
		t.Fatalf("not idempotent: %v", changes)
	}
}

func TestRemovedDefaultStaysRemoved(t *testing.T) {
	// Removed before the upgrade (a v1.0.0 install without Korobeiniki) ...
	dir := t.TempDir()
	writeV100Install(t, dir, map[string]bool{"korobeiniki": true}, nil, nil)
	runSync(t, dir, shipped(t, v100Defaults, v100Files))
	if _, ok := numbers(readCatalog(t, dir))[3]; ok {
		t.Fatal("removed legacy default came back")
	}

	// ... and after it (removed from songs.json once the state exists).
	c := readCatalog(t, dir)
	var kept []Song
	for _, s := range c {
		if s.Default != "bday" {
			kept = append(kept, s)
		}
	}
	data, _ := json.Marshal(kept)
	os.WriteFile(filepath.Join(dir, "songs.json"), data, 0644)
	runSync(t, dir, shipped(t, v100Defaults, v100Files))
	for _, s := range readCatalog(t, dir) {
		if s.Default == "bday" {
			t.Fatal("removed default came back")
		}
	}
}

func TestImprovedDefaultReplacesOnlyUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	runSync(t, dir, shipped(t, v100Defaults, v100Files))
	// The user edits the La Cucaracha track; Korobeiniki stays as installed.
	os.WriteFile(filepath.Join(dir, "songs", "cucaracha-mel.bin"), []byte("MY-CUCA"), 0644)

	improved := map[string]string{}
	for k, v := range v100Files {
		improved[k] = v
	}
	improved["korobeiniki-bass.bin"] = "KORO-BASS-v2"
	improved["cucaracha-mel.bin"] = "CUCA-v2"
	changes := runSync(t, dir, shipped(t, v100Defaults, improved))

	bass, _ := os.ReadFile(filepath.Join(dir, "songs", "korobeiniki-bass.bin"))
	cuca, _ := os.ReadFile(filepath.Join(dir, "songs", "cucaracha-mel.bin"))
	if string(bass) != "KORO-BASS-v2" {
		t.Fatalf("unchanged file not updated: %q (changes %v)", bass, changes)
	}
	if string(cuca) != "MY-CUCA" {
		t.Fatalf("user's file was overwritten: %q", cuca)
	}
}

func TestMigrationNeverUpdatesUnknownFiles(t *testing.T) {
	// A v1.0.0 install whose Korobeiniki bass differs from what ships now:
	// it might be the user's, so it is never replaced.
	dir := t.TempDir()
	writeV100Install(t, dir, nil, nil, nil)
	os.WriteFile(filepath.Join(dir, "songs", "korobeiniki-bass.bin"), []byte("OLD-OR-MINE"), 0644)
	improved := map[string]string{}
	for k, v := range v100Files {
		improved[k] = v
	}
	improved["korobeiniki-bass.bin"] = "KORO-BASS-v2"
	runSync(t, dir, shipped(t, v100Defaults, improved))
	runSync(t, dir, shipped(t, v100Defaults, improved))
	bass, _ := os.ReadFile(filepath.Join(dir, "songs", "korobeiniki-bass.bin"))
	if string(bass) != "OLD-OR-MINE" {
		t.Fatalf("unknown file replaced: %q", bass)
	}
}

func TestNewDefaultFileNameCollision(t *testing.T) {
	dir := t.TempDir()
	user := []Song{{Number: 5, Name: "My Jingle", Tracks: []SongTrack{{File: "jingle-mel.bin"}}}}
	writeV100Install(t, dir, nil, user, map[string]string{"jingle-mel.bin": "MINE"})
	defs := append(append([]embeddedDefault{}, v100Defaults...), def("jinglebells", 5, "1.2.0", "Jingle Bells", "jingle-mel.bin"))
	files := map[string]string{"jingle-mel.bin": "SHIPPED"}
	for k, v := range v100Files {
		files[k] = v
	}
	runSync(t, dir, shipped(t, defs, files))
	mine, _ := os.ReadFile(filepath.Join(dir, "songs", "jingle-mel.bin"))
	if string(mine) != "MINE" {
		t.Fatalf("user's file overwritten: %q", mine)
	}
	c := readCatalog(t, dir)
	i := catalogIndexByDefault(c, "jinglebells")
	if i < 0 || c[i].Number != 6 || !strings.HasPrefix(c[i].Tracks[0].File, "jingle-mel-") {
		t.Fatalf("new default: %+v", c)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "songs", c[i].Tracks[0].File))
	if string(got) != "SHIPPED" {
		t.Fatalf("new default's file: %q", got)
	}
}

func TestAxelFNumberIsNeverHandedOut(t *testing.T) {
	if n := nextSongNumber(nil); n != 2 {
		t.Fatalf("next number on an empty catalog: %d", n)
	}
	if n := nextSongNumber([]Song{{Number: 1, Source: "builtin-package"}}); n != 2 {
		t.Fatalf("next number after Axel F: %d", n)
	}
}

func TestShippedCatalogIsValid(t *testing.T) {
	defs, err := readEmbeddedDefaults(embeddedSongs)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, d := range defs {
		if d.Default == "" || d.Since == "" || keys[d.Default] {
			t.Fatalf("default without key/since, or duplicate key: %+v", d)
		}
		keys[d.Default] = true
		for _, tr := range d.Tracks {
			if _, err := embeddedSongs.ReadFile("songs/" + tr.File); err != nil {
				t.Fatalf("%s: %v", d.Default, err)
			}
		}
	}
}
