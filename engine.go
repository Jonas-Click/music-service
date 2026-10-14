package main

// Engine sound: app.lisp's engine hum (Engine_Sound, 0x4EA), armed for good,
// for one ride, or for a set time. While armed it plays only with the
// kickstand up (vehicle state ready-to-drive); kickstand down (parked)
// silences it until the next kickstand up.
//
// Tunu is unpowered while the scooter is locked and boots with the engine
// sound off at every unlock, so the service keeps the wanted state itself and
// asserts it on every relevant vehicle state change, retrying until
// Engine_Status (0x7ED) confirms it. Nothing is sent while Tunu is unpowered:
// frames that nobody acknowledges walk the CAN controller's error counter
// towards bus-off.

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
	engineRetryInterval   = 2 * time.Second
	engineCmdAttempts     = 3 // ~6s, for interactive commands
	engineBootAttempts    = 8 // ~16s, matches unlockMaxAttempts: Tunu boots after an unlock
	engineStateField      = "state"
	engineMaxDuration     = 24 * time.Hour
	engineMinDuration     = 10 * time.Second
	engineDefaultVolume   = 50
	engineStatusMode      = "engine_mode"
	engineStatusUntil     = "engine_until"
	engineStatusWantVol   = "engine_volume_set"
	engineStatusOn        = "engine_on"
	engineStatusVol       = "engine_volume"
	engineStatusUpdated   = "engine_updated"
	engineStatusDemo      = "engine_demo"
	engineStatusSounding  = "engine_sounding"
	engineStatusWantStyle = "engine_style_set"
	engineStatusStyle     = "engine_style"
	vehicleStateParked    = "parked"
	vehicleStateReady     = "ready-to-drive"
	engineEchoBufferSize  = 8
)

type engineState struct {
	mode   engineMode
	until  time.Time // engineTimed only
	volume int
	style  int // index into engineStyles
	// gen changes on every mode change, so a timer started for an older
	// mode can tell it has been superseded and stop.
	gen int
}

// armed reports whether a mode is active right now.
func (s engineState) armed(now time.Time) bool {
	switch s.mode {
	case engineOn, engineRide:
		return true
	case engineTimed:
		return now.Before(s.until)
	}
	return false
}

// sounding reports whether the engine sound should be playing: armed and the
// kickstand up.
func (s engineState) sounding(now time.Time, vehicleState string) bool {
	return s.armed(now) && vehicleState == vehicleStateReady
}

var engine struct {
	mu sync.Mutex
	s  engineState
	// vehicleState is the last seen vehicle `state`; Tunu is powered only
	// in parked and ready-to-drive.
	vehicleState string
	// assertGen changes whenever a new engineAssert starts, so an older
	// retry loop stops instead of racing it.
	assertGen int
	// demo mirrors Tunu's last Engine_Status: a demo ride is playing.
	demo bool
}

type engineEcho struct {
	on     bool
	volume int
	demo   bool
	style  int // -1: not reported (older app.lisp)
}

// engineStyles are the sound styles app.lisp knows, by their Engine_Sound
// style number (the index).
var engineStyles = []string{"v8", "moped", "sport", "vtwin", "electric"}

func engineStyleID(name string) (int, bool) {
	for i, n := range engineStyles {
		if n == name {
			return i, true
		}
	}
	return 0, false
}

func engineStyleName(id int) string {
	if id >= 0 && id < len(engineStyles) {
		return engineStyles[id]
	}
	return "?"
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
	if c.EngineSound.Style != nil {
		if id, ok := engineStyleID(*c.EngineSound.Style); ok {
			engine.s.style = id
		}
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
	style := engineStyleName(s.style)
	c.EngineSound.Enabled = &enabled
	c.EngineSound.Volume = &volume
	c.EngineSound.Style = &style
}

func vehiclePowered(state string) bool {
	return state == vehicleStateParked || state == vehicleStateReady
}

// engineSet applies a new mode and/or volume (volume < 0 keeps the current
// one), saves the persistent part, and asserts it on Tunu if it is powered.
func engineSet(sink canSink, mode engineMode, d time.Duration, volume int) {
	engine.mu.Lock()
	wasOn := engine.s.sounding(time.Now(), engine.vehicleState)
	engine.s.mode = mode
	engine.s.until = time.Time{}
	if mode == engineTimed {
		engine.s.until = time.Now().Add(d)
	}
	if volume >= 0 {
		engine.s.volume = volume
	}
	engine.s.gen++
	s, vstate := engine.s, engine.vehicleState
	engine.mu.Unlock()

	if mode == engineTimed {
		go engineExpireAfter(sink, s.gen, d)
	}
	engineLogState("ctl", s)
	engineWriteStatus(s, vstate)
	if err := saveConfig(configForSave(), cfgPath); err != nil {
		log.Printf("engine: config save failed: %v", err)
	}
	// Silent before and after (off, kickstand down, or only the volume
	// changed while silent): nothing to tell Tunu. Sending "off" anyway would
	// also stop a running demo ride.
	if !wasOn && !s.sounding(time.Now(), vstate) {
		return
	}
	go engineAssert(sink, engineCmdAttempts)
}

// engineSetStyle changes the sound style. The mode and its timer stay as they
// are; a sounding engine switches style straight away.
func engineSetStyle(sink canSink, id int) {
	engine.mu.Lock()
	engine.s.style = id
	s, vstate := engine.s, engine.vehicleState
	engine.mu.Unlock()
	log.Printf("ctl: engine style -> %s", engineStyleName(id))
	engineWriteStatus(s, vstate)
	if err := saveConfig(configForSave(), cfgPath); err != nil {
		log.Printf("engine: config save failed: %v", err)
	}
	if s.sounding(time.Now(), vstate) {
		go engineAssert(sink, engineCmdAttempts)
	}
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
	wasOn := engine.vehicleState == vehicleStateReady
	engine.s.mode = engineOff
	engine.s.until = time.Time{}
	engine.s.gen++
	s, vstate := engine.s, engine.vehicleState
	engine.mu.Unlock()
	log.Printf("engine: time is up, switching off")
	engineWriteStatus(s, vstate)
	if wasOn {
		engineAssert(sink, engineCmdAttempts)
	}
}

// engineVehicleState follows the vehicle state. Kickstand up (into
// ready-to-drive) starts an armed engine sound, kickstand down (into parked)
// stops it, and losing power (locking) ends a one-ride engine sound.
func engineVehicleState(sink canSink, newState string) {
	now := time.Now()
	engine.mu.Lock()
	old := engine.vehicleState
	if old == newState {
		engine.mu.Unlock()
		return
	}
	engine.vehicleState = newState
	wasPowered, isPowered := vehiclePowered(old), vehiclePowered(newState)
	wasOn := engine.s.sounding(now, old)
	rideEnded := wasPowered && !isPowered && engine.s.mode == engineRide
	if rideEnded {
		engine.s.mode = engineOff
		engine.s.gen++
	}
	s := engine.s
	engine.mu.Unlock()

	engineWriteStatus(s, newState)
	if rideEnded {
		// No CAN frame: Tunu is losing power with the lock anyway.
		log.Printf("engine: ride over (%s -> %s), engine sound off", old, newState)
		return
	}
	isOn := s.sounding(now, newState)
	if !isPowered || wasOn == isOn {
		return
	}
	log.Printf("engine: vehicle %s -> %s, engine sound %s", old, newState, onOff(isOn))
	// Tunu may still be booting if the kickstand goes up right after the
	// unlock, so allow for its boot time.
	go engineAssert(sink, engineBootAttempts)
}

// engineSeed takes the vehicle state when Redis (re)connects. On the first
// connect after the service starts it always tells a powered Tunu the wanted
// state: Tunu keeps running through an MDB crash or reboot, so it may still be
// playing an engine sound this fresh service knows nothing about. After a
// reconnect it is an ordinary state change.
func engineSeed(sink canSink, state string) {
	engine.mu.Lock()
	first := engine.vehicleState == ""
	if first {
		engine.vehicleState = state
	}
	s := engine.s
	engine.mu.Unlock()
	if !first {
		engineVehicleState(sink, state)
		return
	}
	engineWriteStatus(s, state)
	if vehiclePowered(state) {
		log.Printf("engine: service start with Tunu powered (%s), asserting engine sound %s", state, onOff(s.sounding(time.Now(), state)))
		go engineAssert(sink, engineBootAttempts)
	}
}

// engineAssert sends the wanted state until Tunu's Engine_Status confirms it,
// stopping early if a newer engineAssert starts or the vehicle loses power.
func engineAssert(sink canSink, attempts int) {
	engine.mu.Lock()
	engine.assertGen++
	gen := engine.assertGen
	engine.mu.Unlock()
	drainEngineEcho()
	for attempt := 1; attempt <= attempts; attempt++ {
		engine.mu.Lock()
		s, vstate, current := engine.s, engine.vehicleState, engine.assertGen == gen
		engine.mu.Unlock()
		if !current {
			return
		}
		if !vehiclePowered(vstate) {
			log.Printf("engine: vehicle %q, not sending (Tunu is unpowered)", vstate)
			return
		}
		on := s.sounding(time.Now(), vstate)
		cmd := engineCmdOff
		if on {
			cmd = engineCmdOn
		}
		id, data := engineFrame(cmd, s.volume, s.style)
		if err := sink.send(id, data); err != nil {
			log.Printf("engine: send failed (attempt %d/%d): %v", attempt, attempts, err)
		}
		if waitEngineEchoMatch(func(ev engineEcho) bool {
			return ev.on == on && (!on || ev.volume == s.volume) && (ev.style < 0 || ev.style == s.style)
		}, engineRetryInterval) {
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

func waitEngineEchoMatch(match func(engineEcho) bool, d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case ev := <-engineEchoCh:
			if match(ev) {
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
func engineWriteStatus(s engineState, vehicleState string) {
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
		engineStatusMode:      string(s.mode),
		engineStatusUntil:     until,
		engineStatusWantVol:   strconv.Itoa(s.volume),
		engineStatusWantStyle: engineStyleName(s.style),
		engineStatusSounding:  strconv.FormatBool(s.sounding(time.Now(), vehicleState)),
	} {
		if err := conn.hset(statusHash, k, v, ""); err != nil {
			log.Printf("engine: status: hset %s failed: %v", k, err)
		}
	}
}

// engineDemo plays app.lisp's demo ride once (~23 s): the same sound code as a
// real ride, fed by a scripted speed and throttle. It leaves the on/off mode as
// it is. Only sent while Tunu is powered.
func engineDemo(sink canSink) {
	drainEngineEcho()
	for attempt := 1; attempt <= engineCmdAttempts; attempt++ {
		s, vstate := engineSnapshot()
		if !vehiclePowered(vstate) {
			log.Printf("engine: demo: vehicle %q, Tunu is unpowered — unlock the scooter first", vstate)
			return
		}
		id, data := engineFrame(engineCmdDemo, s.volume, s.style)
		if err := sink.send(id, data); err != nil {
			log.Printf("engine: demo: send failed (attempt %d/%d): %v", attempt, engineCmdAttempts, err)
		}
		if waitEngineEchoMatch(func(ev engineEcho) bool { return ev.demo }, engineRetryInterval) {
			log.Printf("engine: demo ride playing, volume %d%%", s.volume)
			return
		}
		log.Printf("engine: demo: not yet confirmed (attempt %d/%d)", attempt, engineCmdAttempts)
	}
	log.Printf("engine: demo never confirmed by Engine_Status (app.lisp without the demo?)")
}

// engineEchoSeen records Tunu's latest Engine_Status for engineStopDemo.
func engineEchoSeen(ev engineEcho) {
	engine.mu.Lock()
	engine.demo = ev.demo
	engine.mu.Unlock()
	select {
	case engineEchoCh <- ev:
	default:
	}
}

// engineStopDemo ends a running demo ride (for `music stop`). Tunu only stops
// the demo on "off", which also switches the engine sound off, so a sounding
// engine is switched straight back on.
func engineStopDemo(sink canSink) {
	engine.mu.Lock()
	demo, s, vstate := engine.demo, engine.s, engine.vehicleState
	engine.mu.Unlock()
	if !demo || !vehiclePowered(vstate) {
		return
	}
	id, data := engineFrame(engineCmdOff, s.volume, s.style)
	if err := sink.send(id, data); err != nil {
		log.Printf("ctl: stop: demo stop failed: %v", err)
		return
	}
	log.Printf("ctl: stop: demo ride stopped")
	if s.sounding(time.Now(), vstate) {
		go engineAssert(sink, engineCmdAttempts)
	}
}

// handleEngineMsg applies "engine:<on|off|ride|for|vol>[:...]" published by
// `music engine`:
//
//	engine:on[:vol]   engine:off   engine:ride[:vol]
//	engine:for:<seconds>[:vol]     engine:vol:<pct>     engine:demo[:vol]
//	engine:style:<name>
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
	case "demo":
		v, ok := vol(2)
		if !ok {
			return
		}
		if v >= 0 {
			s, _ := engineSnapshot()
			engineSet(sink, s.mode, time.Until(s.until), v)
		}
		go engineDemo(sink)
	case "vol":
		v, ok := vol(2)
		if !ok || v < 0 {
			return
		}
		s, _ := engineSnapshot()
		engineSet(sink, s.mode, time.Until(s.until), v)
	case "style":
		if len(parts) < 3 {
			return
		}
		id, ok := engineStyleID(parts[2])
		if !ok {
			log.Printf("ctl: engine: unknown style %q", parts[2])
			return
		}
		engineSetStyle(sink, id)
	default:
		log.Printf("ctl: engine: unknown subcommand %q", parts[1])
	}
}
