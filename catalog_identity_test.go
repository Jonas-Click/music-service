package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSongPackPreservesInstalledCopiesAndNumbers(t *testing.T) {
	defs, err := readEmbeddedDefaults(embeddedSongs)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "songs"), 0700); err != nil {
		t.Fatal(err)
	}
	var original []Song
	files := map[string][]byte{}
	for index, def := range defs {
		song := def.Song
		song.Default = ""
		song.Number = 20 + index
		song.Name = "My " + song.Name
		song.Tracks = append([]SongTrack(nil), song.Tracks...)
		for slot, track := range song.Tracks {
			data, err := fs.ReadFile(embeddedSongs, "songs/"+track.File)
			if err != nil {
				t.Fatal(err)
			}
			name := fmt.Sprintf("custom-%d-%d.bin", index, slot)
			song.Tracks[slot].File = name
			song.Tracks[slot].Volume = 0.4
			if err := os.WriteFile(filepath.Join(dir, "songs", name), data, 0600); err != nil {
				t.Fatal(err)
			}
			files[name] = data
		}
		original = append(original, song)
	}
	if err := writeSongsFile(filepath.Join(dir, "songs.json"), original); err != nil {
		t.Fatal(err)
	}
	if _, err := syncDefaultSongs(dir, embeddedSongs); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "songs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var installed []Song
	if err := json.Unmarshal(data, &installed); err != nil {
		t.Fatal(err)
	}
	if len(installed) != len(original) {
		t.Fatalf("duplicate songs added: %d / %d", len(installed), len(original))
	}
	for index, song := range installed {
		if song.Default != defs[index].Default {
			t.Fatalf("unrecognized song: %#v", song)
		}
		song.Default = ""
		if !reflect.DeepEqual(song, original[index]) {
			t.Fatalf("user settings changed: %#v / %#v", song, original[index])
		}
	}
	for name, expected := range files {
		actual, err := os.ReadFile(filepath.Join(dir, "songs", name))
		if err != nil || !bytes.Equal(expected, actual) {
			t.Fatalf("changed track: %s", name)
		}
	}
	if changes, err := syncDefaultSongs(dir, embeddedSongs); err != nil || len(changes) != 0 {
		t.Fatalf("non-idempotent: %v %v", changes, err)
	}
}
