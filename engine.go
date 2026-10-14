package main

// Engine sound: app.lisp's engine hum (Engine_Sound, 0x4EA), switched on for
// good, for one ride, or for a set time.
//
// Tunu is unpowered while the scooter is locked and boots with the engine
// sound off at every unlock, so the service keeps the wanted state itself and
// re-asserts it whenever the vehicle becomes powered, retrying until
// Engine_Status (0x7ED) confirms it. Nothing is sent while the vehicle is in
// any other state: frames that nobody acknowledges walk the CAN controller's
// error counter towards bus-off.

import (
	"log"
	"strconv"
	"sync"
	"time"
)

type engineMode string

const (
	engineOff   engineMode = "off"
	engineOn    engineMode = "on"    // until switched off; saved in config.json
	engineRide  engineMode = "ride"  // until the scooter is next locked
	engineTimed engineMode = "timed" // until engineState.until
)

const (
	engineRetryInterval  = 2 * time.Second
	engineCmdAttempts    = 3 // ~6s, for interactive commands
	engineBootAttempts   = 8 // ~16s, matches unlockMaxAttempts: Tunu boots after an unlock
	engineStateField     = "state"
	engineMaxDuration    = 24 * time.Hour
	engineMinDuration    = 10 * time.Second
	engineDefaultVolume  = 50
	engineStatusMode     = "engine_mode"
	engineStatusUntil    = "engine_until"
	engineStatusWantVol  = "engine_volume_set"
	engineStatusOn       = "engine_on"
	engineStatusVol      = "engine_volume"
	engineStatusUpdated  = "engine_updated"
	vehicleStateParked   = "parked"
	vehicleStateReady    = "ready-to-drive"
	engineEchoBufferSize = 8
)

type engineState struct {
	mode   engineMode
	until  time.Time // engineTimed only
	volume int
	// gen changes on every state change, so a retry loop or timer started
	// for an older state can tell it has been superseded and stop.
	gen int
}

// wantOn reports whether the engine sound should be playing right now.
func (s engineState) wantOn(now time.Time) bool {
	switch s.mode {
	case engineOn, engineRide:
		return true
	case engineTimed:
		return now.Before(s.until)
	}
	return false
}

var engine struct {
	mu sync.Mutex
	s  engineState
	// vehicleState is the last seen vehicle `state`; Tunu is powered only
	// in parked and ready-to-drive.
	vehicleState string
}

type engineEcho struct {
	on     bool
	volume int
}

var engineEchoCh = make(chan engineEcho, engineEchoBufferSize)

func engineInit(c *Config) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.s = engineState{mode: engineOff, volume: engineDefaultVolume}
	if c.EngineSound.Volume != nil {
		engine.s.volume = *c.EngineSound.Volume
	}
	if c.EngineSound.Enabled != nil && *c.EngineSound.Enabled {
		engine.s.mode = engineOn
	}
}

func engineSnapshot() (engineState, string) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.s, engine.vehicleState
}

// engineToConfig writes the persistent part (the on toggle and the volume)
// into c, for configForSave.
func engineToConfig(c *Config) {
	s, _ := engineSnapshot()
	enabled := s.mode == engineOn
	volume := s.volume
	c.EngineSound.Enabled = &enabled
	c.EngineSound.Volume = &volume
}

func vehiclePowered(state string) bool {
	return state == vehicleStateParked || state == vehicleStateReady
}

// engineSet applies a new mode and/or volume (volume < 0 keeps the current
// one), saves the persistent part, and asserts it on Tunu if it is powered.
func engineSet(sink canSink, mode engineMode, d time.Duration, volume int) {
	engine.mu.Lock()
	engine.s.mode = mode
	engine.s.until = time.Time{}
	if mode == engineTimed {
		engine.s.until = time.Now().Add(d)
	}
	if volume >= 0 {
		engine.s.volume = volume
	}
	engine.s.gen++
	s := engine.s
	engine.mu.Unlock()

	if mode == engineTimed {
		go engineExpireAfter(sink, s.gen, d)
	}
	engineLogState("ctl", s)
	engineWriteStatus(s)
	if err := saveConfig(configForSave(), cfgPath); err != nil {
		log.Printf("engine: config save failed: %v", err)
	}
	go engineAssert(sink, s.gen, engineCmdAttempts)
}

// engineExpireAfter switches a timed engine sound off at its deadline, unless
// something else changed the state first.
func engineExpireAfter(sink canSink, gen int, d time.Duration) {
	select {
	case <-quit:
		return
	case <-time.After(d):
	}
	engine.mu.Lock()
	if engine.s.gen != gen {
		engine.mu.Unlock()
		return
	}
	engine.s.mode = engineOff
	engine.s.until = time.Time{}
	engine.s.gen++
	s := engine.s
	engine.mu.Unlock()
	log.Printf("engine: time is up, switching off")
	engineWriteStatus(s)
	engineAssert(sink, s.gen, engineCmdAttempts)
}

// engineVehicleState follows the vehicle state: an edge into a powered state
// means Tunu has just booted (engine sound off), so the wanted state is
// asserted again; an edge out of it ends a one-ride engine sound.
func engineVehicleState(sink canSink, newState string) {
	engine.mu.Lock()
	old := engine.vehicleState
	engine.vehicleState = newState
	wasPowered, isPowered := vehiclePowered(old), vehiclePowered(newState)
	rideEnded := wasPowered && !isPowered && engine.s.mode == engineRide
	if rideEnded {
		engine.s.mode = engineOff
		engine.s.gen++
	}
	s := engine.s
	engine.mu.Unlock()

	if rideEnded {
		// No CAN frame: Tunu is losing power with the lock anyway.
		log.Printf("engine: ride over (%s -> %s), engine sound off", old, newState)
		engineWriteStatus(s)
		return
	}
	if isPowered && !wasPowered && s.wantOn(time.Now()) {
		log.Printf("engine: vehicle %s -> %s, re-asserting engine sound", old, newState)
		go engineAssert(sink, s.gen, engineBootAttempts)
	}
}

// engineAssert sends the wanted state until Tunu's Engine_Status confirms it,
// stopping early if the state changes again or the vehicle loses power.
func engineAssert(sink canSink, gen int, attempts int) {
	drainEngineEcho()
	for attempt := 1; attempt <= attempts; attempt++ {
		s, vstate := engineSnapshot()
		if s.gen != gen {
			return
		}
		if !vehiclePowered(vstate) {
			log.Printf("engine: vehicle %q, not sending (Tunu is unpowered); will apply at the next unlock", vstate)
			return
		}
		on := s.wantOn(time.Now())
		id, data := engineFrame(on, s.volume)
		if err := sink.send(id, data); err != nil {
			log.Printf("engine: send failed (attempt %d/%d): %v", attempt, attempts, err)
		}
		if waitEngineEcho(on, s.volume, engineRetryInterval) {
			log.Printf("engine: confirmed %s, volume %d%% (attempt %d/%d)", onOff(on), s.volume, attempt, attempts)
			return
		}
		log.Printf("engine: not yet confirmed (attempt %d/%d)", attempt, attempts)
	}
	log.Printf("engine: never confirmed by Engine_Status after %d attempts", attempts)
}

func drainEngineEcho() {
	for {
		select {
		case <-engineEchoCh:
		default:
			return
		}
	}
}

func waitEngineEcho(on bool, volume int, d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case ev := <-engineEchoCh:
			if ev.on == on && (!on || ev.volume == volume) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func engineLogState(who string, s engineState) {
	switch s.mode {
	case engineTimed:
		log.Printf("%s: engine sound on until %s, volume %d%%", who, s.until.Format("15:04:05"), s.volume)
	case engineRide:
		log.Printf("%s: engine sound on for this ride, volume %d%%", who, s.volume)
	default:
		log.Printf("%s: engine sound %s, volume %d%%", who, s.mode, s.volume)
	}
}

// engineWriteStatus publishes the wanted state into the status hash, for
// `music engine`. Tunu's confirmation lands there separately, from canReader.
func engineWriteStatus(s engineState) {
	conn, err := redisConnect(cfg.Redis.Addr)
	if err != nil {
		log.Printf("engine: status: redis connect failed: %v", err)
		return
	}
	defer conn.close()
	until := "0"
	if s.mode == engineTimed {
		until = strconv.FormatInt(s.until.Unix(), 10)
	}
	for k, v := range map[string]string{
		engineStatusMode:    string(s.mode),
		engineStatusUntil:   until,
		engineStatusWantVol: strconv.Itoa(s.volume),
	} {
		if err := conn.hset(statusHash, k, v, ""); err != nil {
			log.Printf("engine: status: hset %s failed: %v", k, err)
		}
	}
}

// handleEngineMsg applies "engine:<on|off|ride|for|vol>[:...]" published by
// `music engine`:
//
//	engine:on[:vol]   engine:off   engine:ride[:vol]
//	engine:for:<seconds>[:vol]     engine:vol:<pct>
func handleEngineMsg(sink canSink, parts []string) {
	if len(parts) < 2 {
		return
	}
	vol := func(i int) (int, bool) {
		if len(parts) <= i || parts[i] == "" {
			return -1, true
		}
		v, err := strconv.Atoi(parts[i])
		if err != nil || v < 0 || v > 100 {
			log.Printf("ctl: engine: invalid volume %q", parts[i])
			return 0, false
		}
		return v, true
	}
	switch parts[1] {
	case "on":
		if v, ok := vol(2); ok {
			engineSet(sink, engineOn, 0, v)
		}
	case "off":
		engineSet(sink, engineOff, 0, -1)
	case "ride":
		if v, ok := vol(2); ok {
			engineSet(sink, engineRide, 0, v)
		}
	case "for":
		if len(parts) < 3 {
			return
		}
		secs, err := strconv.Atoi(parts[2])
		d := time.Duration(secs) * time.Second
		if err != nil || d < engineMinDuration || d > engineMaxDuration {
			log.Printf("ctl: engine: invalid duration %q", parts[2])
			return
		}
		if v, ok := vol(3); ok {
			engineSet(sink, engineTimed, d, v)
		}
	case "vol":
		v, ok := vol(2)
		if !ok || v < 0 {
			return
		}
		s, _ := engineSnapshot()
		engineSet(sink, s.mode, time.Until(s.until), v)
	default:
		log.Printf("ctl: engine: unknown subcommand %q", parts[1])
	}
}
