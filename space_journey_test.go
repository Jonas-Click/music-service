package main

import (
	"bytes"
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSpaceJourneyTracksFitController(t *testing.T) {
	for _, test := range []struct {
		name string
		size int
	}{
		{"tng-theme-mel.bin", 1930},
		{"tng-theme-bass.bin", 727},
		{"star-trek-fanfare.bin", 106},
	} {
		data, err := fs.ReadFile(embeddedSongs, "songs/"+test.name)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) != test.size || len(data) > maxSongBytes || len(data) < 26 {
			t.Fatalf("%s: invalid size %d", test.name, len(data))
		}
		if string(data[:4]) != "MThd" || binary.BigEndian.Uint32(data[4:8]) != 6 ||
			binary.BigEndian.Uint16(data[8:10]) != 0 || binary.BigEndian.Uint16(data[10:12]) != 1 ||
			string(data[14:18]) != "MTrk" || int(binary.BigEndian.Uint32(data[18:22])) != len(data)-22 ||
			!bytes.Equal(data[len(data)-4:], []byte{0, 0xff, 0x2f, 0}) {
			t.Fatalf("%s: invalid type-0 single-track MIDI", test.name)
		}
	}
}

func TestSpaceJourneyUpgradePreservesUserSongs(t *testing.T) {
	defs, err := readEmbeddedDefaults(embeddedSongs)
	if err != nil {
		t.Fatal(err)
	}
	var beforeDefs []embeddedDefault
	beforeFiles := map[string]string{}
	for _, d := range defs {
		if d.Default == "tng-theme" || d.Default == "star-trek-fanfare" {
			continue
		}
		beforeDefs = append(beforeDefs, d)
		for _, track := range d.Tracks {
			data, err := fs.ReadFile(embeddedSongs, "songs/"+track.File)
			if err != nil {
				t.Fatal(err)
			}
			beforeFiles[track.File] = string(data)
		}
	}
	dir := t.TempDir()
	if _, err := syncDefaultSongs(dir, shipped(t, beforeDefs, beforeFiles)); err != nil {
		t.Fatal(err)
	}
	before := readCatalog(t, dir)
	user := Song{Number: 9, Name: "My song", Tracks: []SongTrack{{File: "my-song.bin", Volume: 0.4, Decay: 0.8}}}
	before = append(before, user)
	custom := []byte("user-owned track")
	if err := os.WriteFile(filepath.Join(dir, "songs", "my-song.bin"), custom, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeSongsFile(filepath.Join(dir, "songs.json"), before); err != nil {
		t.Fatal(err)
	}
	if _, err := syncDefaultSongs(dir, embeddedSongs); err != nil {
		t.Fatal(err)
	}
	after := readCatalog(t, dir)
	if len(after) != len(before)+2 {
		t.Fatalf("unexpected catalogue length: %d", len(after))
	}
	if !reflect.DeepEqual(after[:len(before)], before) {
		t.Fatal("existing song entries changed")
	}
	if after[len(before)].Number != 10 || after[len(before)].Name != "Space Journey" ||
		after[len(before)+1].Number != 11 || after[len(before)+1].Name != "Space Journey Fanfare" {
		t.Fatalf("unexpected new entries: %#v", after[len(before):])
	}
	data, err := os.ReadFile(filepath.Join(dir, "songs", "my-song.bin"))
	if err != nil || !bytes.Equal(data, custom) {
		t.Fatal("custom track changed")
	}
	if changes, err := syncDefaultSongs(dir, embeddedSongs); err != nil || len(changes) != 0 {
		t.Fatalf("repeat update: %v %v", changes, err)
	}
}
