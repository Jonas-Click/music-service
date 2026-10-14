package main

// The on-disk configuration: its shape, the defaults applied to missing
// fields, and a crash-safe save.
//
// cfg is loaded once at startup and then treated as read-only; anything that
// changes at runtime lives in liveSettings (settings.go) instead.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	CAN struct {
		Interface string `json:"interface"`
	} `json:"can"`
	Redis struct {
		Addr string `json:"addr"`
	} `json:"redis"`
	Status struct {
		// How long a status-hash entry is considered fresh. `music status`
		// warns past this age instead of printing a stale echo as if it were
		// current.
		StaleSeconds int `json:"stale_seconds"`
	} `json:"status"`
	UnlockMelody struct {
		Enabled *bool `json:"enabled"`
		Song    int   `json:"song"`
		// Volume to set before playing, 0-100. 0 means "don't touch the
		// current volume". A pointer, like TriggerFrom below, so an explicit
		// 0 (leave-as-is) survives a reload instead of being silently
		// overwritten back to the default every time the config is read.
		Volume *int `json:"volume"`
		// How long to let the melody play before sending stop, in seconds.
		// 0 means don't auto-stop — let it play out on its own. Also a
		// pointer, for the same reason as Volume.
		DurationSeconds *int `json:"duration_seconds"`
		// Which vehicle hash field to watch, and the value that counts as
		// the unlock edge. Defaults to a field/value pair that corresponds
		// to an unlock on the vehicle's own state hash — see README.md for
		// how to confirm this against a running vehicle, and how to point
		// it at a different field (e.g. a handlebar lock sensor) if the
		// physical-unlock moment is wanted instead of the command that
		// starts an unlock.
		TriggerField string `json:"trigger_field"`
		TriggerValue string `json:"trigger_value"`
		// The value trigger_field must be transitioning FROM for this to
		// count as an unlock. Without it, any transition into trigger_value
		// fires the melody — including, on the default state/parked pair,
		// ending a ride (ready-to-drive -> parked), which is not an unlock.
		// Defaults to "stand-by", the state a genuine unlock actually starts
		// from. A pointer so an explicit "" (meaning: fire on any transition
		// into trigger_value, the old behavior) is distinguishable from the
		// field being absent (meaning: apply the "stand-by" default).
		TriggerFrom *string `json:"trigger_from"`
	} `json:"unlock_melody"`
	// EngineSound holds the persistent part of the engine-sound state: the
	// on/off toggle and the volume. One-ride and timed modes are runtime
	// only (engine.go).
	EngineSound struct {
		Enabled *bool   `json:"enabled"`
		Volume  *int    `json:"volume"`
		Style   *string `json:"style"`
	} `json:"engine_sound"`
}

var (
	// cfg is written once by main() before any goroutine starts and is
	// read-only thereafter. Anything mutable at runtime lives in
	// liveSettings (settings.go).
	cfg     *Config
	cfgPath string
)

const defaultConfigPath = "/data/music-service/config.json"

func loadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open config %s: %w", path, err)
	}
	defer f.Close()

	var c Config
	if err := json.NewDecoder(f).Decode(&c); err != nil {
		return nil, fmt.Errorf("config parse error: %w", err)
	}

	if c.CAN.Interface == "" {
		c.CAN.Interface = "can0"
	}
	if c.Redis.Addr == "" {
		c.Redis.Addr = "127.0.0.1:6379"
	}
	if c.Status.StaleSeconds <= 0 {
		c.Status.StaleSeconds = 5
	}
	if c.UnlockMelody.Enabled == nil {
		f := false
		c.UnlockMelody.Enabled = &f
	}
	if c.UnlockMelody.Song == 0 {
		c.UnlockMelody.Song = 3 // Korobeiniki in the default song pack
	}
	if c.UnlockMelody.Volume == nil {
		v := 100
		c.UnlockMelody.Volume = &v
	}
	if c.UnlockMelody.DurationSeconds == nil {
		d := 15
		c.UnlockMelody.DurationSeconds = &d
	}
	if c.UnlockMelody.TriggerField == "" {
		c.UnlockMelody.TriggerField = "state"
	}
	if c.UnlockMelody.TriggerValue == "" {
		c.UnlockMelody.TriggerValue = "parked"
	}
	if c.UnlockMelody.TriggerFrom == nil {
		f := "stand-by"
		c.UnlockMelody.TriggerFrom = &f
	}
	if c.EngineSound.Enabled == nil {
		f := false
		c.EngineSound.Enabled = &f
	}
	if c.EngineSound.Volume == nil {
		v := 50
		c.EngineSound.Volume = &v
	}
	if c.EngineSound.Style == nil {
		st := engineStyles[0]
		c.EngineSound.Style = &st
	}
	return &c, nil
}

// saveConfig writes the config atomically: temp file in the same directory,
// fsync, rename. A plain os.WriteFile truncates the real file first, so a
// power loss mid-write (routine on a scooter, and this runs on every
// `music settings` change) would leave a config.json that fails to parse
// and the service then refuses to start.
func saveConfig(c *Config, path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0644); err != nil { // CreateTemp makes it 0600
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	tmpName = ""

	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
