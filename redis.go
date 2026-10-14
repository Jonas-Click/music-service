package main

// The Redis side of the daemon: watching the "music" channel for CLI
// commands, watching "vehicle" for the unlock trigger, and publishing the
// status hash from whatever Tunu echoes back on CAN.
//
// A hash update publishes only the field name that changed, so a watcher
// re-reads the value with HMGET rather than trusting the payload.

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Bare resource name, matching the vehicle's own Redis convention
	// (`vehicle`, `engine-ecu`, `battery:0`, ...) rather than a
	// process-qualified name — confirmed against the real keyspace on a
	// running MDB, where every other key follows this pattern.
	cmdChannel  = "music" // `music play/stop/vol/raw` publish here
	statusHash  = "music" // daemon-owned; `music status` reads it directly
	vehicleHash = "vehicle"
	vehicleChan = "vehicle"
)

func redisWatcher(sink canSink) {
	for {
		select {
		case <-quit:
			return
		default:
		}
		if err := redisWatchLoop(sink); err != nil {
			log.Printf("redis: %v — retry in 3s", err)
		}
		select {
		case <-quit:
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func redisWatchLoop(sink canSink) error {
	subConn, err := redisConnect(cfg.Redis.Addr)
	if err != nil {
		return fmt.Errorf("sub connect: %w", err)
	}
	defer subConn.close()

	cmdConn, err := redisConnect(cfg.Redis.Addr)
	if err != nil {
		return fmt.Errorf("cmd connect: %w", err)
	}
	defer cmdConn.close()

	// Seed the baseline value of whatever field the unlock trigger is
	// currently watching, so a daemon restart while the scooter is already
	// unlocked does not replay the melody — only an actual edge into
	// trigger_value, observed after this point, fires it.
	triggerField := live.get().unlockTriggerField
	if vals, err := cmdConn.hmget(vehicleHash, triggerField); err == nil && len(vals) >= 1 {
		lastVehicleField[triggerField] = vals[0]
	}
	// The engine sound follows the vehicle state too; seeding it here also
	// asserts a wanted engine sound if Tunu is already powered.
	if vals, err := cmdConn.hmget(vehicleHash, engineStateField); err == nil && len(vals) >= 1 {
		engineVehicleState(sink, vals[0])
	}
	es, vstate := engineSnapshot()
	engineWriteStatus(es, vstate)

	channels := []string{"SUBSCRIBE", cmdChannel, vehicleChan}
	subConn.setTimeout(cmdTimeout)
	if err := subConn.send(channels...); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	for i := 1; i < len(channels); i++ {
		if _, err := subConn.readReply(); err != nil {
			return fmt.Errorf("subscribe ack for %s: %w", channels[i], err)
		}
	}
	subConn.setTimeout(0)
	log.Printf("redis connected (%s), watching %s + %s", cfg.Redis.Addr, cmdChannel, vehicleChan)

	for {
		select {
		case <-quit:
			return nil
		default:
		}

		subConn.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		reply, err := subConn.readReply()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return err
		}

		arr, ok := reply.([]interface{})
		if !ok || len(arr) < 3 {
			continue
		}
		if t, _ := arr[0].(string); t != "message" {
			continue
		}
		channel, _ := arr[1].(string)
		payload, _ := arr[2].(string)

		switch channel {
		case cmdChannel:
			handleCommand(sink, cmdConn, payload)
		case vehicleChan:
			handleVehicleField(sink, cmdConn, payload)
		}
	}
}

// lastVehicleField holds the last observed value per watched field, so the
// unlock trigger can tell an edge (just became "parked") from a level
// (already "parked", republished for an unrelated reason).
var lastVehicleField = map[string]string{}

func handleVehicleField(sink canSink, cmdConn *redisConn, field string) {
	if field == engineStateField {
		if vals, err := cmdConn.hmget(vehicleHash, field); err == nil && len(vals) >= 1 {
			engineVehicleState(sink, vals[0])
		}
	}
	s := live.get()
	if field != s.unlockTriggerField {
		return
	}
	vals, err := cmdConn.hmget(vehicleHash, field)
	if err != nil || len(vals) < 1 {
		return
	}
	newVal := vals[0]
	oldVal := lastVehicleField[field]
	lastVehicleField[field] = newVal

	if !s.unlockEnabled || newVal != s.unlockTriggerValue || oldVal == s.unlockTriggerValue {
		return
	}
	// trigger_from empty means the old permissive behavior: any transition
	// into trigger_value counts. Set (the "stand-by" default included), it
	// must match exactly — otherwise, on the default state/parked pair,
	// ending a ride (ready-to-drive -> parked) fires the melody just as
	// wrongly as a genuine unlock (stand-by -> parked) would.
	if s.unlockTriggerFrom != "" && oldVal != s.unlockTriggerFrom {
		log.Printf("unlock trigger: %s %q -> %q ignored — not from %q", field, oldVal, newVal, s.unlockTriggerFrom)
		return
	}
	log.Printf("unlock trigger: %s %q -> %q — playing song %d", field, oldVal, newVal, s.unlockSong)
	// Runs in its own goroutine: the retry loop (and the optional auto-stop
	// timer after it) can take several seconds to tens of seconds, and
	// blocking here would stall this connection's single subscriber loop —
	// no other command or vehicle field update would be processed until it
	// finished.
	go playUnlockMelodyWithRetry(sink, s.unlockSong, s.unlockVolume, s.unlockDurationSeconds)
}

// echoEvent is a parsed Music_Status (0x7E6) frame, published by canReader
// so other goroutines can wait for a specific song to actually be confirmed
// playing instead of just trusting that a CAN send landed.
type echoEvent struct {
	playing bool
	song    int
}

// Buffered so canReader's send never blocks; nothing drains it when no
// retry loop is running, so old entries are simply dropped once full —
// they're already captured in the status hash, this channel only exists to
// let a waiter be woken immediately instead of polling.
var echoCh = make(chan echoEvent, 8)

// uploadAckEvent is a parsed Music_Upload_Status (0x7E7) frame — the same
// request/echo pattern as echoEvent, just for upload completion instead of
// playback state.
type uploadAckEvent struct {
	songNumber int
	trackSlot  int
	result     int
}

var uploadAckCh = make(chan uploadAckEvent, 8)

const (
	uploadRetryTimeout = 2 * time.Second
	// Gap between chunk frames, doubled on each retry: Tunu's LispBM event
	// queue drops frames that arrive faster than its handler runs (3ms and
	// 5ms both lost frames on a 700-byte track). 8ms is ~0.8s per such track.
	uploadChunkGap = 8 * time.Millisecond
	// Two budgets, same split as cmdMaxAttempts/unlockMaxAttempts below:
	// a short one for interactive commands, a patient one for the unlock
	// trigger, which has to tolerate Tunu's own boot time.
	uploadMaxAttempts     = 3 // ~6s total
	uploadBootMaxAttempts = 8 // ~16s total — matches unlockMaxAttempts
)

// uploadMu serializes uploads: Tunu's receiver state (upload-active-slot,
// upload-checksum, upload-received) is a single set of globals with no
// per-session identity, and unlike every other command here a song upload
// is multiple frames, not one — so two uploadSong calls from different
// goroutines (an interactive `music play` racing the unlock trigger, which
// runs in its own goroutine precisely so it doesn't block the main command
// loop) could interleave their Begin/Chunk/End frames and corrupt each
// other's upload. This restores the "single writer" property the rest of
// this service already relies on for every other command.
var uploadMu sync.Mutex

// uploadSong streams every track of song over CAN (Begin -> chunks -> End
// per track), retrying a track from Begin if its End isn't acked OK within
// uploadRetryTimeout — there's no per-chunk ack, only this end-of-track
// check, so a failure just means redoing the whole (small) track rather
// than something fancier. "source": "builtin-package" songs (Axel F) are
// skipped entirely: Tunu already has those from its own package import,
// nothing to stream. maxAttempts lets callers choose the interactive vs
// boot-tolerant budget above.
func uploadSong(sink canSink, song Song, maxAttempts int) error {
	if song.Source == "builtin-package" {
		return nil
	}
	uploadMu.Lock()
	defer uploadMu.Unlock()
	for slot, track := range song.Tracks {
		data, err := songTrackBytes(track)
		if err != nil {
			return fmt.Errorf("song %d track %d: %w", song.Number, slot, err)
		}
		if err := uploadTrack(sink, song.Number, slot, data, track.Volume, track.Decay, maxAttempts); err != nil {
			return fmt.Errorf("song %d track %d: %w", song.Number, slot, err)
		}
	}
	return nil
}

func uploadTrack(sink canSink, songNumber, trackSlot int, data []byte, volume, decay float64, maxAttempts int) error {
	checksum := byteSum(data)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		drainUploadAckChannel()
		if err := sendTrackFrames(sink, songNumber, trackSlot, data, volume, decay, checksum, chunkGap(attempt)); err != nil {
			log.Printf("upload: song %d track %d send failed (attempt %d/%d): %v", songNumber, trackSlot, attempt, maxAttempts, err)
			time.Sleep(uploadRetryTimeout)
			continue
		}
		if waitForUploadAck(songNumber, trackSlot, uploadRetryTimeout) {
			return nil
		}
		log.Printf("upload: song %d track %d not acked (attempt %d/%d), retrying", songNumber, trackSlot, attempt, maxAttempts)
	}
	return fmt.Errorf("never acked OK after %d attempts", maxAttempts)
}

// chunkGap doubles the base gap per retry, capped at 4x, so the long unlock
// retry budget never stretches a single upload into minutes.
func chunkGap(attempt int) time.Duration {
	shift := attempt - 1
	if shift > 2 {
		shift = 2
	}
	return uploadChunkGap << shift
}

func sendTrackFrames(sink canSink, songNumber, trackSlot int, data []byte, volume, decay float64, checksum byte, gap time.Duration) error {
	id, frame := uploadBeginFrame(songNumber, trackSlot, len(data), volume, decay)
	if err := sink.send(id, frame); err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	for seq, off := 0, 0; off < len(data); seq++ {
		end := off + uploadChunkPayload
		if end > len(data) {
			end = len(data)
		}
		id, frame := uploadChunkFrame(seq, data[off:end])
		if err := sink.send(id, frame); err != nil {
			return fmt.Errorf("chunk: %w", err)
		}
		off = end
		time.Sleep(gap)
	}
	id, frame = uploadEndFrame(songNumber, trackSlot, checksum)
	if err := sink.send(id, frame); err != nil {
		return fmt.Errorf("end: %w", err)
	}
	return nil
}

func drainUploadAckChannel() {
	for {
		select {
		case <-uploadAckCh:
		default:
			return
		}
	}
}

func waitForUploadAck(songNumber, trackSlot int, d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case ev := <-uploadAckCh:
			if ev.songNumber == songNumber && ev.trackSlot == trackSlot {
				if ev.result != uploadOK {
					log.Printf("upload: song %d track %d rejected by Tunu, result %d (1 checksum, 2 length, 3 rejected)", songNumber, trackSlot, ev.result)
				}
				return ev.result == uploadOK
			}
		case <-deadline:
			return false
		}
	}
}

const (
	unlockRetryInterval = 2 * time.Second
	unlockMaxAttempts   = 8 // ~16s total — generous allowance for Tunu's own boot time
)

// playUnlockMelodyWithRetry sends the unlock melody and waits for Tunu's own
// Music_Status echo to confirm it actually landed, resending if not. Once
// confirmed, if durationSeconds > 0 it schedules a stop after that many
// seconds instead of letting the song play out on its own.
//
// The vehicle unlocking and Tunu's lisp script finishing its own boot are
// two independent things — this script is large enough that a fresh boot
// measurably takes a few seconds to finish loading, and a fire-and-forget
// send during that window reaches a controller that isn't listening yet and
// does nothing, silently. Retrying against the actual echo rather than a
// fixed delay means this self-corrects regardless of how long boot happens
// to take on a given run.
func playUnlockMelodyWithRetry(sink canSink, songNumber, volume, durationSeconds int) {
	song, ok := songByNumber(songNumber)
	if !ok {
		log.Printf("unlock melody: song %d not found in catalog", songNumber)
		return
	}

	drainEchoChannel()
	// Upload once, with its own boot-tolerant retry budget — if this
	// succeeds, Tunu has definitely received and acked the buffer, so the
	// play-confirm loop below only has to handle the much narrower race
	// between upload completion and the play command landing, not the
	// full boot-time race too.
	if err := uploadSong(sink, song, uploadBootMaxAttempts); err != nil {
		log.Printf("unlock melody: upload never succeeded, giving up: %v", err)
		return
	}

	for attempt := 1; attempt <= unlockMaxAttempts; attempt++ {
		if volume > 0 {
			if err := sendVolume(sink, volume); err != nil {
				log.Printf("unlock melody: volume set failed (attempt %d/%d): %v", attempt, unlockMaxAttempts, err)
			}
		}
		if err := sendPlay(sink, songNumber); err != nil {
			log.Printf("unlock melody: play failed (attempt %d/%d): %v", attempt, unlockMaxAttempts, err)
		}

		if waitForEcho(songNumber, unlockRetryInterval) {
			log.Printf("unlock melody: confirmed playing song %d (attempt %d/%d)", songNumber, attempt, unlockMaxAttempts)
			if durationSeconds > 0 {
				log.Printf("unlock melody: stopping in %ds", durationSeconds)
				time.Sleep(time.Duration(durationSeconds) * time.Second)
				if err := sendStop(sink); err != nil {
					log.Printf("unlock melody: auto-stop failed: %v", err)
				} else {
					log.Printf("unlock melody: auto-stopped after %ds", durationSeconds)
				}
			}
			return
		}
		log.Printf("unlock melody: no echo yet for song %d (attempt %d/%d) — Tunu may still be booting, retrying", songNumber, attempt, unlockMaxAttempts)
	}
	log.Printf("unlock melody: gave up after %d attempts — no Music_Status echo ever confirmed song %d", unlockMaxAttempts, songNumber)
}

func drainEchoChannel() {
	for {
		select {
		case <-echoCh:
		default:
			return
		}
	}
}

// waitForEchoMatch blocks up to d for an echo satisfying match, ignoring any
// echo that doesn't rather than treating it as confirmation or giving up
// early on it.
//
// echoCh has exactly one reader's worth of fan-out: if two confirmation
// waiters are running at once (e.g. a `music stop` lands right as the
// unlock trigger fires), each echo goes to whichever one happens to receive
// it first, not to every waiter that wants it. In practice that just costs
// the other waiter one retry round rather than a false result, and
// concurrent waiters are rare enough (interactive commands, one unlock per
// session) that a real multi-waiter fan-out isn't worth the complexity here.
func waitForEchoMatch(match func(echoEvent) bool, d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case ev := <-echoCh:
			if match(ev) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func waitForEcho(song int, d time.Duration) bool {
	return waitForEchoMatch(func(ev echoEvent) bool { return ev.playing && ev.song == song }, d)
}

const (
	cmdRetryInterval = 1500 * time.Millisecond
	cmdMaxAttempts   = 3 // ~4.5s total — a short retry margin for interactive commands
)

// confirmPlaying waits for Tunu's echo to confirm song is actually playing,
// resending if not. Same race the unlock trigger guards against (a command
// landing before Tunu is listening, or just getting lost), with a shorter
// budget since this runs from an interactive CLI call rather than at boot —
// and it's what keeps the status hash from going stale when a send doesn't
// land: canReader only updates it from a real echo, so a command with no
// retry and no echo just leaves the old status sitting there indefinitely.
func confirmPlaying(sink canSink, song int) {
	drainEchoChannel()
	for attempt := 1; attempt <= cmdMaxAttempts; attempt++ {
		if waitForEcho(song, cmdRetryInterval) {
			log.Printf("ctl: play song %d confirmed (attempt %d/%d)", song, attempt, cmdMaxAttempts)
			return
		}
		if attempt == cmdMaxAttempts {
			break
		}
		log.Printf("ctl: play song %d not yet confirmed (attempt %d/%d), resending", song, attempt, cmdMaxAttempts)
		if err := sendPlay(sink, song); err != nil {
			log.Printf("ctl: play %d resend failed: %v", song, err)
		}
	}
	log.Printf("ctl: play song %d never confirmed by Tunu's echo after %d attempts", song, cmdMaxAttempts)
}

// confirmStopped is confirmPlaying's counterpart for `music stop`.
func confirmStopped(sink canSink) {
	drainEchoChannel()
	notPlaying := func(ev echoEvent) bool { return !ev.playing }
	for attempt := 1; attempt <= cmdMaxAttempts; attempt++ {
		if waitForEchoMatch(notPlaying, cmdRetryInterval) {
			log.Printf("ctl: stop confirmed (attempt %d/%d)", attempt, cmdMaxAttempts)
			return
		}
		if attempt == cmdMaxAttempts {
			break
		}
		log.Printf("ctl: stop not yet confirmed (attempt %d/%d), resending", attempt, cmdMaxAttempts)
		if err := sendStop(sink); err != nil {
			log.Printf("ctl: stop resend failed: %v", err)
		}
	}
	log.Printf("ctl: stop never confirmed by Tunu's echo after %d attempts", cmdMaxAttempts)
}

func handleCommand(sink canSink, cmdConn *redisConn, payload string) {
	parts := strings.SplitN(payload, ":", 3)
	if len(parts) == 0 {
		return
	}
	switch parts[0] {
	case "play":
		if len(parts) < 2 {
			return
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || !validSong(n) {
			log.Printf("ctl: play: invalid song %q", parts[1])
			return
		}
		song, _ := songByNumber(n)
		if err := uploadSong(sink, song, uploadMaxAttempts); err != nil {
			log.Printf("ctl: play %d upload failed: %v", n, err)
			return
		}
		if err := sendPlay(sink, n); err != nil {
			log.Printf("ctl: play %d failed: %v", n, err)
			return
		}
		log.Printf("ctl: play -> song %d (%s)", n, songName(n))
		go confirmPlaying(sink, n)

	case "stop":
		if err := sendStop(sink); err != nil {
			log.Printf("ctl: stop failed: %v", err)
			return
		}
		log.Printf("ctl: stop")
		engineStopDemo(sink)
		go confirmStopped(sink)

	case "vol":
		if len(parts) < 2 {
			return
		}
		pct, err := strconv.Atoi(parts[1])
		if err != nil || pct < 0 || pct > 100 {
			log.Printf("ctl: vol: invalid percentage %q", parts[1])
			return
		}
		if err := sendVolume(sink, pct); err != nil {
			log.Printf("ctl: vol %d failed: %v", pct, err)
			return
		}
		log.Printf("ctl: vol -> %d%%", pct)

	case "raw":
		if len(parts) < 2 {
			return
		}
		rest := strings.SplitN(parts[1], ":", 2)
		idVal, err := strconv.ParseUint(rest[0], 16, 32)
		if err != nil {
			log.Printf("ctl: raw: invalid CAN ID %q", rest[0])
			return
		}
		hexBytes := "01"
		if len(rest) > 1 {
			hexBytes = rest[1]
		}
		data, err := parseHexBytes(hexBytes)
		if err != nil {
			log.Printf("ctl: raw: %v", err)
			return
		}
		if err := sink.send(uint32(idVal), data); err != nil {
			log.Printf("ctl: raw 0x%X failed: %v", idVal, err)
			return
		}
		log.Printf("ctl: raw -> id=0x%X data=% X", idVal, data)

	case "settings":
		handleSettingsMsg(parts)

	case "engine":
		handleEngineMsg(sink, strings.Split(payload, ":"))
	}
}

func parseHexBytes(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("odd-length hex string %q", s)
	}
	out := make([]byte, len(s)/2)
	for i := range out {
		v, err := strconv.ParseUint(s[i*2:i*2+2], 16, 8)
		if err != nil {
			return nil, fmt.Errorf("bad hex byte %q: %w", s[i*2:i*2+2], err)
		}
		out[i] = byte(v)
	}
	return out, nil
}

func sendPlay(sink canSink, song int) error {
	id, data := playFrame(song)
	return sink.send(id, data)
}

func sendStop(sink canSink) error {
	id, data := stopFrame()
	return sink.send(id, data)
}

func sendVolume(sink canSink, pct int) error {
	id, data := volumeFrame(pct)
	return sink.send(id, data)
}

// handleSettingsMsg applies "settings:<key>:<value>" published by `music
// settings <key> <value>`.
func handleSettingsMsg(parts []string) {
	if len(parts) < 3 {
		return
	}
	key, value := parts[1], parts[2]

	switch key {
	case "unlock.enabled":
		b := value == "true"
		live.update(func(s *settings) { s.unlockEnabled = b })
		log.Printf("settings: unlock.enabled -> %v", b)

	case "unlock.song":
		n, err := strconv.Atoi(value)
		if err != nil || !validSong(n) {
			log.Printf("settings: unlock.song: invalid value %q", value)
			return
		}
		live.update(func(s *settings) { s.unlockSong = n })
		log.Printf("settings: unlock.song -> %d (%s)", n, songName(n))

	case "unlock.volume":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 100 {
			log.Printf("settings: unlock.volume: invalid value %q", value)
			return
		}
		live.update(func(s *settings) { s.unlockVolume = n })
		log.Printf("settings: unlock.volume -> %d", n)

	case "unlock.duration":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 300 {
			log.Printf("settings: unlock.duration: invalid value %q (0-300)", value)
			return
		}
		live.update(func(s *settings) { s.unlockDurationSeconds = n })
		if n == 0 {
			log.Printf("settings: unlock.duration -> 0 (play to completion, no auto-stop)")
		} else {
			log.Printf("settings: unlock.duration -> %ds", n)
		}

	case "unlock.trigger_field":
		live.update(func(s *settings) { s.unlockTriggerField = value })
		log.Printf("settings: unlock.trigger_field -> %s (takes effect after a reconnect/restart)", value)

	case "unlock.trigger_value":
		live.update(func(s *settings) { s.unlockTriggerValue = value })
		log.Printf("settings: unlock.trigger_value -> %s", value)

	case "unlock.trigger_from":
		live.update(func(s *settings) { s.unlockTriggerFrom = value })
		if value == "" {
			log.Printf("settings: unlock.trigger_from -> (any) — fires on any transition into trigger_value")
		} else {
			log.Printf("settings: unlock.trigger_from -> %s", value)
		}

	default:
		log.Printf("settings: unknown key %q", key)
		return
	}

	if err := saveConfig(configForSave(), cfgPath); err != nil {
		log.Printf("settings: save failed: %v", err)
	}
}

// canReader continuously reads frames from sink and republishes Music_Status
// (0x7E6) echoes into the status hash. It exits quietly if the sink has no
// receive path at all (log:/null:), signaled by errNoRx.
func canReader(sink canSink) {
	cmdConn, err := redisConnect(cfg.Redis.Addr)
	if err != nil {
		log.Printf("status: redis connect failed: %v", err)
		return
	}
	defer cmdConn.close()

	for {
		select {
		case <-quit:
			return
		default:
		}
		id, data, err := sink.recv()
		if err != nil {
			if err == errNoRx {
				log.Printf("status: %s has no receive path — status hash will not update", sink.describe())
				return
			}
			log.Printf("status: recv: %v", err)
			continue
		}

		if id == canIDUploadStatus && len(data) >= canUploadStatusLen {
			select {
			case uploadAckCh <- uploadAckEvent{songNumber: int(data[0]), trackSlot: int(data[1]), result: int(data[2])}:
			default:
			}
			continue
		}

		if id == canIDEngineStatus && len(data) >= canEngineStatusLen {
			on, volume := data[0] != 0, int(data[1])
			demo := len(data) >= 3 && data[2] != 0
			engineEchoSeen(engineEcho{on: on, volume: volume, demo: demo})
			for k, v := range map[string]string{
				engineStatusOn:      strconv.FormatBool(on),
				engineStatusVol:     strconv.Itoa(volume),
				engineStatusDemo:    strconv.FormatBool(demo),
				engineStatusUpdated: strconv.FormatInt(time.Now().Unix(), 10),
			} {
				if err := cmdConn.hset(statusHash, k, v, ""); err != nil {
					log.Printf("status: hset %s failed: %v", k, err)
				}
			}
			continue
		}

		if id != canIDStatus || len(data) < canStatusLen {
			continue
		}

		playing := data[0] != 0
		song := int(data[1])
		volume := int(data[2])
		name := ""
		if playing && validSong(song) {
			name = songName(song)
		}

		select {
		case echoCh <- echoEvent{playing: playing, song: song}:
		default:
		}

		fields := map[string]string{
			"playing": strconv.FormatBool(playing),
			"song":    strconv.Itoa(song),
			"name":    name,
			"volume":  strconv.Itoa(volume),
			"updated": strconv.FormatInt(time.Now().Unix(), 10),
		}
		for k, v := range fields {
			if err := cmdConn.hset(statusHash, k, v, ""); err != nil {
				log.Printf("status: hset %s failed: %v", k, err)
			}
		}
	}
}
