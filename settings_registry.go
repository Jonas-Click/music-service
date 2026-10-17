package main

// Every setting in config.json, in one table. `music settings` lists, checks
// and sets them through it, the service applies and saves them through it,
// and the help text and shell completion are generated from it, so a setting
// is defined in exactly one place. `music vol` and `music engine ...` are
// shortcuts that end in the same state and the same save.

import (
	"fmt"
	"log"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type settingDef struct {
	key     string
	desc    string
	choices []string // fixed values, for help and completion; nil: free
	// restart: saved now, used after the next service restart.
	restart bool
	show    func(c *Config) string       // the value as saved in config.json
	check   func(v string) error         // shared by the CLI and the service
	apply   func(sink canSink, v string) // service side; v already checked
}

var boolChoices = []string{"true", "false"}

func checkBool(v string) error {
	if v != "true" && v != "false" {
		return fmt.Errorf("must be true or false")
	}
	return nil
}

func checkRange(lo, hi int) func(string) error {
	return func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			return fmt.Errorf("must be a number from %d to %d", lo, hi)
		}
		return nil
	}
}

func checkNotEmpty(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("must not be empty")
	}
	return nil
}

func checkAny(string) error { return nil }

var ifaceName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

func atoi(v string) int { n, _ := strconv.Atoi(v); return n }

func showInt(p *int, def int) string {
	if p == nil {
		return strconv.Itoa(def)
	}
	return strconv.Itoa(*p)
}

func showBool(p *bool) string { return strconv.FormatBool(p != nil && *p) }

var settingsRegistry = []settingDef{
	{
		key: "music.volume", desc: "Music volume 0-100, kept across unlocks (same as music vol)",
		show:  func(c *Config) string { return showInt(c.Music.Volume, 100) },
		check: checkRange(0, 100),
		apply: func(sink canSink, v string) { applyMusicVolumeSetting(sink, atoi(v)) },
	},
	{
		key: "unlock.enabled", desc: "Play a melody when the scooter unlocks", choices: boolChoices,
		show:  func(c *Config) string { return showBool(c.UnlockMelody.Enabled) },
		check: checkBool,
		apply: func(_ canSink, v string) { live.update(func(s *settings) { s.unlockEnabled = v == "true" }) },
	},
	{
		key: "unlock.song", desc: `Song to play on unlock (see "music songs")`,
		show: func(c *Config) string {
			return fmt.Sprintf("%d (%s)", c.UnlockMelody.Song, songName(c.UnlockMelody.Song))
		},
		check: func(v string) error {
			if n, err := strconv.Atoi(v); err != nil || !validSong(n) {
				return fmt.Errorf(`not a song number — run "music songs" to list them`)
			}
			return nil
		},
		apply: func(_ canSink, v string) { live.update(func(s *settings) { s.unlockSong = atoi(v) }) },
	},
	{
		key: "unlock.volume", desc: "Unlock melody volume 0-100, 0 = the music volume",
		show:  func(c *Config) string { return showInt(c.UnlockMelody.Volume, 100) },
		check: checkRange(0, 100),
		apply: func(_ canSink, v string) { live.update(func(s *settings) { s.unlockVolume = atoi(v) }) },
	},
	{
		key: "unlock.duration", desc: "Stop the unlock melody after N seconds (0-300), 0 = play it all",
		show:  func(c *Config) string { return showInt(c.UnlockMelody.DurationSeconds, 15) },
		check: checkRange(0, 300),
		apply: func(_ canSink, v string) { live.update(func(s *settings) { s.unlockDurationSeconds = atoi(v) }) },
	},
	{
		key: "unlock.trigger_field", desc: "vehicle hash field to watch (advanced)", restart: true,
		show:  func(c *Config) string { return c.UnlockMelody.TriggerField },
		check: checkNotEmpty,
		apply: func(_ canSink, v string) { live.update(func(s *settings) { s.unlockTriggerField = v }) },
	},
	{
		key: "unlock.trigger_value", desc: "Value that counts as the unlock (advanced)",
		show:  func(c *Config) string { return c.UnlockMelody.TriggerValue },
		check: checkNotEmpty,
		apply: func(_ canSink, v string) { live.update(func(s *settings) { s.unlockTriggerValue = v }) },
	},
	{
		key: "unlock.trigger_from", desc: `Value it must change from (advanced), "" = any`,
		show: func(c *Config) string {
			if c.UnlockMelody.TriggerFrom == nil {
				return ""
			}
			return *c.UnlockMelody.TriggerFrom
		},
		check: checkAny,
		apply: func(_ canSink, v string) { live.update(func(s *settings) { s.unlockTriggerFrom = v }) },
	},
	{
		key: "engine.enabled", desc: "Engine sound on until switched off (music engine on/off)", choices: boolChoices,
		show:  func(c *Config) string { return showBool(c.EngineSound.Enabled) },
		check: checkBool,
		apply: func(sink canSink, v string) {
			if v == "true" {
				engineApply(sink, engineOn, 0, -1)
			} else {
				engineApply(sink, engineOff, 0, -1)
			}
		},
	},
	{
		key: "engine.volume", desc: "Engine sound volume 0-100",
		show:  func(c *Config) string { return showInt(c.EngineSound.Volume, engineDefaultVolume) },
		check: checkRange(0, 100),
		apply: func(sink canSink, v string) {
			s, _ := engineSnapshot()
			engineApply(sink, s.mode, time.Until(s.until), atoi(v))
		},
	},
	{
		key: "engine.style", desc: "Engine sound style", choices: engineStyles,
		show: func(c *Config) string {
			if c.EngineSound.Style == nil {
				return engineStyles[0]
			}
			return *c.EngineSound.Style
		},
		check: func(v string) error {
			if _, ok := engineStyleID(v); !ok {
				return fmt.Errorf("must be one of %s", strings.Join(engineStyles, ", "))
			}
			return nil
		},
		apply: func(sink canSink, v string) { id, _ := engineStyleID(v); engineApplyStyle(sink, id) },
	},
	{
		key: "status.stale_seconds", desc: "music status calls a reply stale after N seconds (1-3600)",
		show:  func(c *Config) string { return strconv.Itoa(c.Status.StaleSeconds) },
		check: checkRange(1, 3600),
		apply: func(_ canSink, v string) { live.update(func(s *settings) { s.staleSeconds = atoi(v) }) },
	},
	{
		key: "can.interface", desc: "CAN interface", restart: true,
		show: func(c *Config) string { return c.CAN.Interface },
		check: func(v string) error {
			if !ifaceName.MatchString(v) {
				return fmt.Errorf("not an interface name")
			}
			return nil
		},
		apply: func(_ canSink, v string) { live.update(func(s *settings) { s.canInterface = v }) },
	},
	{
		key: "redis.addr", desc: "Redis address host:port", restart: true,
		show: func(c *Config) string { return c.Redis.Addr },
		check: func(v string) error {
			host, port, err := net.SplitHostPort(v)
			if err != nil || host == "" {
				return fmt.Errorf("must be host:port")
			}
			if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
				return fmt.Errorf("bad port %q", port)
			}
			return nil
		},
		apply: func(_ canSink, v string) { live.update(func(s *settings) { s.redisAddr = v }) },
	},
}

func settingByKey(key string) (settingDef, bool) {
	for _, d := range settingsRegistry {
		if d.key == key {
			return d, true
		}
	}
	return settingDef{}, false
}

// saveSettings is the one save path: the live settings merged into the
// config, written atomically.
func saveSettings() {
	if err := saveConfig(configForSave(), cfgPath); err != nil {
		log.Printf("settings: save failed: %v", err)
	}
}

// settingsHelp is the settings part of the help, generated from the registry.
func settingsHelp() string {
	var sb strings.Builder
	for _, d := range settingsRegistry {
		desc := d.desc
		if d.choices != nil && !(len(d.choices) == 2 && d.choices[0] == "true") {
			desc += ": " + strings.Join(d.choices, ", ")
		}
		if d.restart {
			desc += " (after a restart)"
		}
		fmt.Fprintf(&sb, "  %-22s %s\n", d.key, desc)
	}
	return sb.String()
}
