package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Section and field names in config.json that differ from the setting keys
// (the keys are the short names `music settings` has always used).
var keySection = map[string]string{"unlock_melody": "unlock", "engine_sound": "engine"}
var keyField = map[string]string{"duration_seconds": "duration"}

func configLeafKeys(t *testing.T) []string {
	t.Helper()
	c, err := loadConfigFrom(t, "{}")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(c)
	var tree map[string]map[string]any
	if err := json.Unmarshal(data, &tree); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for section, fields := range tree {
		s := section
		if k, ok := keySection[s]; ok {
			s = k
		}
		for field := range fields {
			f := field
			if k, ok := keyField[f]; ok {
				f = k
			}
			keys = append(keys, s+"."+f)
		}
	}
	sort.Strings(keys)
	return keys
}

func loadConfigFrom(t *testing.T, content string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(content), 0644)
	return loadConfig(p)
}

func TestEverySettingIsSettable(t *testing.T) {
	keys := configLeafKeys(t)
	if len(keys) != len(settingsRegistry) {
		t.Errorf("config.json has %d settings, the registry %d: %v", len(keys), len(settingsRegistry), keys)
	}
	for _, k := range keys {
		if _, ok := settingByKey(k); !ok {
			t.Errorf("config.json field %s has no `music settings` key", k)
		}
	}
	seen := map[string]bool{}
	for _, d := range settingsRegistry {
		if seen[d.key] {
			t.Errorf("duplicate key %s", d.key)
		}
		seen[d.key] = true
		if d.show == nil || d.check == nil || d.apply == nil {
			t.Errorf("%s is missing show/check/apply", d.key)
		}
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte("{}"), 0644)
	var err error
	if cfg, err = loadConfig(cfgPath); err != nil {
		t.Fatal(err)
	}
	cfg.Redis.Addr = "127.0.0.1:1" // engine status writes must fail fast, not reach a real Redis
	live.init(cfg)
	engineInit(cfg)
	songCatalog = []Song{{Number: 2, Name: "La Cucaracha"}, {Number: 7, Name: "Mine"}}

	values := map[string]string{
		"music.volume": "35", "unlock.enabled": "true", "unlock.song": "7",
		"unlock.volume": "80", "unlock.duration": "12", "unlock.trigger_field": "state",
		"unlock.trigger_value": "parked", "unlock.trigger_from": "",
		"engine.enabled": "true", "engine.volume": "65", "engine.style": "vtwin",
		"status.stale_seconds": "9", "can.interface": "can1", "redis.addr": "10.0.0.5:6380",
	}
	sink := newNullSink()
	for _, d := range settingsRegistry {
		v, ok := values[d.key]
		if !ok {
			t.Fatalf("no test value for %s", d.key)
		}
		if err := d.check(v); err != nil {
			t.Fatalf("%s %q rejected: %v", d.key, v, err)
		}
		handleSettingsMsg(sink, []string{"settings", d.key, v})
	}

	saved, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range settingsRegistry {
		got := d.show(saved)
		if want := values[d.key]; got != want && !strings.HasPrefix(got, want+" ") {
			t.Errorf("%s: saved %q, want %q", d.key, got, want)
		}
	}
	// Boot-time settings are saved but not used before a restart.
	if cfg.CAN.Interface == "can1" || cfg.Redis.Addr == "10.0.0.5:6380" {
		t.Error("a restart-only setting changed the running config")
	}
}
