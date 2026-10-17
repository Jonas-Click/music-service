package main

import "sync"

// Runtime-mutable settings — the unlock-melody configuration, which `music
// settings unlock.*` can change while the service runs. Everything else
// (can.interface, redis.addr) is boot-time only: edit config.json and
// restart.
//
// Reads take one snapshot per access rather than locking per field, so the
// unlock-trigger handler never sees half of one update and half of the next.
type liveSettings struct {
	mu sync.RWMutex
	s  settings
}

type settings struct {
	unlockEnabled         bool
	unlockSong            int
	unlockVolume          int
	unlockDurationSeconds int
	unlockTriggerField    string
	unlockTriggerValue    string
	unlockTriggerFrom     string
	musicVolume           int
	// Boot-time settings: saved when changed, used after a restart (the
	// running service keeps cfg's values).
	staleSeconds int
	canInterface string
	redisAddr    string
}

var live liveSettings

func (l *liveSettings) init(c *Config) {
	l.mu.Lock()
	defer l.mu.Unlock()
	triggerFrom := ""
	if c.UnlockMelody.TriggerFrom != nil {
		triggerFrom = *c.UnlockMelody.TriggerFrom
	}
	volume := 0
	if c.UnlockMelody.Volume != nil {
		volume = *c.UnlockMelody.Volume
	}
	duration := 0
	if c.UnlockMelody.DurationSeconds != nil {
		duration = *c.UnlockMelody.DurationSeconds
	}
	musicVolume := 100
	if c.Music.Volume != nil {
		musicVolume = *c.Music.Volume
	}
	l.s = settings{
		staleSeconds:          c.Status.StaleSeconds,
		canInterface:          c.CAN.Interface,
		redisAddr:             c.Redis.Addr,
		musicVolume:           musicVolume,
		unlockEnabled:         c.UnlockMelody.Enabled != nil && *c.UnlockMelody.Enabled,
		unlockSong:            c.UnlockMelody.Song,
		unlockVolume:          volume,
		unlockDurationSeconds: duration,
		unlockTriggerField:    c.UnlockMelody.TriggerField,
		unlockTriggerValue:    c.UnlockMelody.TriggerValue,
		unlockTriggerFrom:     triggerFrom,
	}
}

func (l *liveSettings) get() settings {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.s
}

func (l *liveSettings) update(fn func(*settings)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fn(&l.s)
}

// configForSave merges the immutable config with the live settings, so what
// gets persisted matches what the service is actually doing.
func configForSave() *Config {
	c := *cfg
	s := live.get()
	enabled := s.unlockEnabled
	c.UnlockMelody.Enabled = &enabled
	c.UnlockMelody.Song = s.unlockSong
	volume := s.unlockVolume
	c.UnlockMelody.Volume = &volume
	duration := s.unlockDurationSeconds
	c.UnlockMelody.DurationSeconds = &duration
	c.UnlockMelody.TriggerField = s.unlockTriggerField
	c.UnlockMelody.TriggerValue = s.unlockTriggerValue
	triggerFrom := s.unlockTriggerFrom
	c.UnlockMelody.TriggerFrom = &triggerFrom
	musicVolume := s.musicVolume
	c.Music.Volume = &musicVolume
	c.Status.StaleSeconds = s.staleSeconds
	c.CAN.Interface = s.canInterface
	c.Redis.Addr = s.redisAddr
	engineToConfig(&c)
	return &c
}
