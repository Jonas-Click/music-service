package main

// The command-line side: `music play|stop|vol|raw|status`, `music settings
// ...`, and help text.
//
// The CLI never touches CAN directly — it publishes to the music channel
// and the daemon executes, so there is exactly one writer for the bus.
// `status` is the one read-only exception: it reads the daemon-owned status
// hash directly, with no round trip through the daemon.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func runCtl(args []string) {
	songsPath := filepath.Join(filepath.Dir(cfgPath), "songs.json")
	if err := loadSongs(songsPath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: songs: %v\n", err)
	}
	rebuildHelpText()

	if len(args) == 0 {
		fmt.Print(helpText)
		os.Exit(0)
	}
	cmd := args[0]

	switch cmd {
	case "help", "--help", "-h":
		fmt.Print(helpText)
		os.Exit(0)
	case "--version", "-v", "version":
		fmt.Println(version)
		os.Exit(0)
	case "settings":
		runSettings(args[1:])
		return
	case "status":
		runStatus()
		return
	case "engine":
		runEngine(args[1:])
		return
	case "songs":
		if len(args) >= 2 && args[1] == "add" {
			runSongsAdd(args[2:], songsPath)
			return
		}
		if len(args) >= 2 && (args[1] == "remove" || args[1] == "rm") {
			runSongsRemove(args[2:], songsPath)
			return
		}
		fmt.Print(songListText())
		return
	case "play":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: music play <song number>")
			os.Exit(1)
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || !validSong(n) {
			fmt.Fprintln(os.Stderr, "invalid song number — run `music songs` to list valid numbers")
			os.Exit(1)
		}
		publishAndConfirm(fmt.Sprintf("play:%d", n), fmt.Sprintf("play -> song %d (%s)", n, songName(n)))

	case "stop":
		publishAndConfirm("stop", "stop")

	case "vol":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: music vol <0-100>")
			os.Exit(1)
		}
		p, err := strconv.Atoi(args[1])
		if err != nil || p < 0 || p > 100 {
			fmt.Fprintln(os.Stderr, "volume must be 0-100")
			os.Exit(1)
		}
		publishAndConfirm(fmt.Sprintf("vol:%d", p), fmt.Sprintf("volume -> %d%%", p))

	case "raw":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: music raw <canid-hex> [hexbytes]")
			os.Exit(1)
		}
		msg := "raw:" + args[1]
		hexBytes := "01"
		if len(args) >= 3 {
			hexBytes = args[2]
			msg += ":" + hexBytes
		} else {
			msg += ":01"
		}
		publishAndConfirm(msg, fmt.Sprintf("raw -> id=%s data=%s", args[1], hexBytes))

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %q\n\n%s", cmd, helpText)
		os.Exit(1)
	}
}

func runStatus() {
	addr := "127.0.0.1:6379"
	if c, err := loadConfig(cfgPath); err == nil {
		addr = c.Redis.Addr
	}
	conn, err := redisConnect(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to Redis at %s: %v\n", addr, err)
		os.Exit(1)
	}
	defer conn.close()

	fields, err := conn.hgetall(statusHash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading status: %v\n", err)
		os.Exit(1)
	}
	if len(fields) == 0 {
		fmt.Println("no status echo received yet from Tunu (Music_Status / 0x7E6)")
		fmt.Println("this is expected right after a fresh install, before anything has")
		fmt.Println("been played. Playback control does not depend on this echo — only")
		fmt.Println("this status readback does.")
		return
	}

	updated, _ := strconv.ParseInt(fields["updated"], 10, 64)
	age := time.Since(time.Unix(updated, 0))
	stale := ""
	staleSeconds := 5
	if c, err := loadConfig(cfgPath); err == nil {
		staleSeconds = c.Status.StaleSeconds
	}
	if updated == 0 || age > time.Duration(staleSeconds)*time.Second {
		stale = fmt.Sprintf("  (stale — last echo %s ago)", age.Round(time.Second))
	}

	playing := fields["playing"] == "true"
	fmt.Printf("playing:  %v%s\n", playing, stale)
	if playing {
		fmt.Printf("song:     %s (%s)\n", fields["song"], fields["name"])
	}
	fmt.Printf("volume:   %s%%\n", fields["volume"])
}

// runEngine handles `music engine [on [vol] | off | ride [vol] | for <dur> [vol]
// | vol <pct>]`; with no arguments it shows the current state.
func runEngine(args []string) {
	if len(args) == 0 {
		runEngineStatus()
		return
	}
	volArg := func(i int) string {
		if len(args) <= i {
			return ""
		}
		v, err := strconv.Atoi(args[i])
		if err != nil || v < 0 || v > 100 {
			fmt.Fprintln(os.Stderr, "volume must be 0-100")
			os.Exit(1)
		}
		return strconv.Itoa(v)
	}
	withVol := func(v string) string {
		if v == "" {
			return ""
		}
		return fmt.Sprintf(", volume %s%%", v)
	}
	switch args[0] {
	case "on":
		v := volArg(1)
		publishAndConfirm("engine:on:"+v, "engine sound on until switched off"+withVol(v))
	case "off":
		publishAndConfirm("engine:off", "engine sound off")
	case "ride":
		v := volArg(1)
		publishAndConfirm("engine:ride:"+v, "engine sound on until the scooter is locked"+withVol(v))
	case "for":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: music engine for <duration, e.g. 10m, 90s, 1h; a plain number is minutes> [vol]")
			os.Exit(1)
		}
		d, err := parseEngineDuration(args[1])
		if err != nil || d < engineMinDuration || d > engineMaxDuration {
			fmt.Fprintf(os.Stderr, "duration must be between %s and %s (e.g. 10m, 90s, 1h)\n", engineMinDuration, engineMaxDuration)
			os.Exit(1)
		}
		v := volArg(2)
		publishAndConfirm(fmt.Sprintf("engine:for:%d:%s", int(d.Seconds()), v),
			fmt.Sprintf("engine sound on for %s%s", d, withVol(v)))
	case "vol":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: music engine vol <0-100>")
			os.Exit(1)
		}
		v := volArg(1)
		publishAndConfirm("engine:vol:"+v, "engine volume -> "+v+"%")
	case "demo":
		v := volArg(1)
		publishAndConfirm("engine:demo:"+v, "engine demo ride (~23 s)"+withVol(v))
	case "help", "--help", "-h":
		fmt.Print(engineUsage)
	default:
		fmt.Fprintf(os.Stderr, "unknown engine command: %q\n\n%s", args[0], engineUsage)
		os.Exit(1)
	}
}

// parseEngineDuration accepts Go durations ("10m", "90s", "1h30m") and a plain
// number, which means minutes.
func parseEngineDuration(s string) (time.Duration, error) {
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Minute, nil
	}
	return time.ParseDuration(s)
}

func runEngineStatus() {
	addr := "127.0.0.1:6379"
	if c, err := loadConfig(cfgPath); err == nil {
		addr = c.Redis.Addr
	}
	conn, err := redisConnect(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to Redis at %s: %v\n", addr, err)
		os.Exit(1)
	}
	defer conn.close()
	f, err := conn.hgetall(statusHash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading status: %v\n", err)
		os.Exit(1)
	}

	mode := f[engineStatusMode]
	switch mode {
	case "":
		fmt.Println("mode:     unknown (is music-service running?)")
	case string(engineTimed):
		until, _ := strconv.ParseInt(f[engineStatusUntil], 10, 64)
		left := time.Until(time.Unix(until, 0)).Round(time.Second)
		fmt.Printf("mode:     on for a set time, %s left (until %s)\n", left, time.Unix(until, 0).Format("15:04:05"))
	case string(engineRide):
		fmt.Println("mode:     on for this ride (off when locked)")
	case string(engineOn):
		fmt.Println("mode:     on until switched off")
	default:
		fmt.Println("mode:     off")
	}
	if v := f[engineStatusWantVol]; v != "" {
		fmt.Printf("volume:   %s%%\n", v)
	}

	updated, _ := strconv.ParseInt(f[engineStatusUpdated], 10, 64)
	if updated == 0 {
		fmt.Println("tunu:     no Engine_Status echo yet (Tunu off, or its app.lisp has no engine sound)")
		return
	}
	demo := ""
	if f[engineStatusDemo] == "true" {
		demo = ", demo ride playing"
	}
	fmt.Printf("tunu:     %s, volume %s%%%s (confirmed %s ago)\n",
		map[bool]string{true: "on", false: "off"}[f[engineStatusOn] == "true"],
		f[engineStatusVol], demo, time.Since(time.Unix(updated, 0)).Round(time.Second))
}

func runSettings(args []string) {
	if len(args) == 0 {
		c, err := loadConfig(cfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading config: %v\n", err)
			os.Exit(1)
		}
		enabled := c.UnlockMelody.Enabled != nil && *c.UnlockMelody.Enabled
		triggerFrom := ""
		if c.UnlockMelody.TriggerFrom != nil {
			triggerFrom = *c.UnlockMelody.TriggerFrom
		}
		triggerFromDisplay := triggerFrom
		if triggerFromDisplay == "" {
			triggerFromDisplay = "(any)"
		}
		volume := 0
		if c.UnlockMelody.Volume != nil {
			volume = *c.UnlockMelody.Volume
		}
		duration := 0
		if c.UnlockMelody.DurationSeconds != nil {
			duration = *c.UnlockMelody.DurationSeconds
		}

		type row struct{ key, val, desc string }
		rows := []row{
			{"unlock.enabled", strconv.FormatBool(enabled), "Play a melody when the vehicle unlocks"},
			{"unlock.song", fmt.Sprintf("%d (%s)", c.UnlockMelody.Song, songName(c.UnlockMelody.Song)), "Song to play on unlock"},
			{"unlock.volume", fmt.Sprintf("%d", volume), "Volume to set first, 0 = leave as-is"},
			{"unlock.duration", fmt.Sprintf("%ds", duration), "Auto-stop after N seconds, 0 = play to completion"},
			{"unlock.trigger_field", c.UnlockMelody.TriggerField, "vehicle hash field to watch"},
			{"unlock.trigger_from", triggerFromDisplay, "required prior value, empty = any"},
			{"unlock.trigger_value", c.UnlockMelody.TriggerValue, "value that counts as the unlock edge"},
		}

		const kw, vw = 22, 20
		sep := "  " + strings.Repeat("-", kw) + "  " + strings.Repeat("-", vw) + "  " + strings.Repeat("-", 40)
		fmt.Print("\n=== Settings ===\n\n")
		fmt.Printf("  %-*s  %-*s  %s\n", kw, "KEY", vw, "VALUE", "DESCRIPTION")
		fmt.Println(sep)
		for _, r := range rows {
			fmt.Printf("  %-*s  %-*s  %s\n", kw, r.key, vw, r.val, r.desc)
		}
		fmt.Printf("\n  can.interface=%s  redis.addr=%s  (config.json + restart to change)\n",
			c.CAN.Interface, c.Redis.Addr)
		fmt.Printf("\n  Edit: music settings <key> <value>\n\n")
		return
	}

	key := args[0]
	if key == "--help" || key == "-h" || key == "help" {
		fmt.Print(settingsUsage)
		return
	}
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "setting %q requires a value\n", key)
		os.Exit(1)
	}
	value := args[1]

	validKeys := map[string]bool{
		"unlock.enabled": true, "unlock.song": true, "unlock.volume": true,
		"unlock.duration": true, "unlock.trigger_field": true, "unlock.trigger_value": true,
		"unlock.trigger_from": true,
	}
	if !validKeys[key] {
		fmt.Fprintf(os.Stderr, "unknown setting: %q\n\n%s", key, settingsUsage)
		os.Exit(1)
	}

	publishAndConfirm("settings:"+key+":"+value, fmt.Sprintf("%s -> %s", key, value))
}

func publishAndConfirm(msg, okMsg string) {
	addr := "127.0.0.1:6379"
	if c, err := loadConfig(cfgPath); err == nil {
		addr = c.Redis.Addr
	}

	conn, err := redisConnect(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to Redis at %s: %v\n", addr, err)
		os.Exit(1)
	}
	defer conn.close()
	conn.setTimeout(cmdTimeout)

	n, err := conn.publish(cmdChannel, msg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "publish failed: %v\n", err)
		os.Exit(1)
	}
	if n == 0 {
		fmt.Fprintln(os.Stderr, "warning: no subscribers — is music-service running?")
		os.Exit(1)
	}
	fmt.Printf("ok: %s\n", okMsg)
}

// runSongsAdd handles `music songs add <number> <name> <melody-file>
// [volume] [decay] [--bass <bass-file> [volume] [decay]]` — the mechanical
// part of adding a local song (copying file(s) into songs/ and updating
// songs.json) without hand-editing JSON, for one or two tracks. It does
// NOT reduce/clean a MIDI file for you — see README.md for preparing one;
// this just validates size/format and wires it in.
func runSongsAdd(args []string, songsPath string) {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, `usage: music songs add <number> <name> <melody-file> [volume] [decay] [--bass <bass-file> [volume] [decay]]

Adds a song to your local pack (songs.json + songs/), without needing to
hand-edit JSON. See README.md for the full walkthrough, including how to
prepare a MIDI file. Requires "systemctl restart music-service" to take
effect. volume/decay default to 1.5/0.92 for melody, 0.7/0.92 for bass —
the same as every built-in multi-track song.

Examples:
  music songs add 5 "My Song" my-song.mid
  music songs add 5 "My Song" my-song-mel.mid 1.5 0.92 --bass my-song-bass.mid 0.7 0.92`)
		os.Exit(1)
	}

	number, err := strconv.Atoi(args[0])
	if err != nil || number <= 0 {
		fmt.Fprintln(os.Stderr, "invalid song number — must be a positive integer")
		os.Exit(1)
	}
	if number == axelfReservedNumber {
		fmt.Fprintf(os.Stderr, "song number %d is reserved for Axel F's special-cased package-import path on the Tunu side — pick a different number\n", axelfReservedNumber)
		fmt.Fprintln(os.Stderr, "(Axel F isn't in the default catalog, but Tunu's app.lisp still treats this number specially — see README.md)")
		os.Exit(1)
	}
	if _, taken := songByNumber(number); taken {
		fmt.Fprintf(os.Stderr, "song number %d is already in use (%s) — pick a different number\n", number, songName(number))
		os.Exit(1)
	}
	name := args[1]

	bassAt := -1
	for i, a := range args {
		if a == "--bass" {
			bassAt = i
			break
		}
	}
	melodyArgs := args[2:]
	var bassArgs []string
	if bassAt >= 0 {
		melodyArgs = args[2:bassAt]
		bassArgs = args[bassAt+1:]
		if len(bassArgs) < 1 {
			fmt.Fprintln(os.Stderr, "--bass requires a MIDI file")
			os.Exit(1)
		}
	}
	if len(melodyArgs) < 1 {
		fmt.Fprintln(os.Stderr, "missing melody MIDI file")
		os.Exit(1)
	}

	slug := slugify(name)
	dir := filepath.Dir(songsPath)
	if err := os.MkdirAll(filepath.Join(dir, "songs"), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create songs directory: %v\n", err)
		os.Exit(1)
	}

	tracks := []SongTrack{addSongTrack(melodyArgs, slug+"-mel.bin", 1.5, 0.92, dir)}
	if bassAt >= 0 {
		tracks = append(tracks, addSongTrack(bassArgs, slug+"-bass.bin", 0.7, 0.92, dir))
	}

	songCatalog = append(songCatalog, Song{Number: number, Name: name, Tracks: tracks})
	if err := saveSongs(songsPath); err != nil {
		fmt.Fprintf(os.Stderr, "cannot save %s: %v\n", songsPath, err)
		os.Exit(1)
	}

	fmt.Printf("ok: added song %d (%s), %d track(s)\n", number, name, len(tracks))
	fmt.Println("restart music-service for it to take effect: systemctl restart music-service")
}

// addSongTrack validates trackArgs[0] (a MIDI file path, with optional
// [volume] [decay] after it), copies it to dir/songs/fileName, and returns
// the resulting SongTrack. Exits the process on any failure, same as the
// rest of runSongsAdd — this only ever runs as a one-shot CLI command, so
// there's no caller that needs a recoverable error here.
func addSongTrack(trackArgs []string, fileName string, defaultVolume, defaultDecay float64, dir string) SongTrack {
	midiPath := trackArgs[0]
	volume, decay := defaultVolume, defaultDecay
	if len(trackArgs) >= 2 {
		v, err := strconv.ParseFloat(trackArgs[1], 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid volume %q\n", trackArgs[1])
			os.Exit(1)
		}
		volume = v
	}
	if len(trackArgs) >= 3 {
		d, err := strconv.ParseFloat(trackArgs[2], 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid decay %q\n", trackArgs[2])
			os.Exit(1)
		}
		decay = d
	}

	data, err := os.ReadFile(midiPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read %s: %v\n", midiPath, err)
		os.Exit(1)
	}
	if len(data) < 4 || string(data[:4]) != "MThd" {
		fmt.Fprintf(os.Stderr, "%s doesn't look like a standard MIDI file (missing the \"MThd\" header)\n", midiPath)
		os.Exit(1)
	}
	if len(data) > maxSongBytes {
		fmt.Fprintf(os.Stderr, "%s is %d bytes — Tunu can only buffer %d bytes per track\n", midiPath, len(data), maxSongBytes)
		fmt.Fprintln(os.Stderr, "reduce it to a single melody line first (a short, monophonic track) — see README.md")
		os.Exit(1)
	}

	destPath := filepath.Join(dir, "songs", fileName)
	if err := os.WriteFile(destPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "cannot write %s: %v\n", destPath, err)
		os.Exit(1)
	}
	fmt.Printf("  -> songs/%s (volume=%v decay=%v)\n", fileName, volume, decay)

	return SongTrack{File: fileName, Volume: volume, Decay: decay}
}

// runSongsRemove handles `music songs remove <number>` (alias `rm`) —
// removes the catalog entry and deletes its track file(s) from songs/,
// unless another song still references the same file (shouldn't normally
// happen, since each song's files are uniquely named, but cheap to check).
func runSongsRemove(args []string, songsPath string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, `usage: music songs remove <number>

Removes a song from your local pack (songs.json) and deletes its track
file(s) from songs/. Requires "systemctl restart music-service" to take
effect.`)
		os.Exit(1)
	}

	number, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid song number — must be an integer")
		os.Exit(1)
	}
	song, ok := songByNumber(number)
	if !ok {
		fmt.Fprintf(os.Stderr, "song number %d not found — run \"music songs\" to list what's there\n", number)
		os.Exit(1)
	}

	remaining := make([]Song, 0, len(songCatalog))
	for _, s := range songCatalog {
		if s.Number != number {
			remaining = append(remaining, s)
		}
	}
	songCatalog = remaining

	dir := filepath.Dir(songsPath)
	for _, t := range song.Tracks {
		if songFileStillReferenced(t.File) {
			continue
		}
		path := filepath.Join(dir, "songs", t.File)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "warning: could not remove %s: %v\n", path, err)
		}
	}

	if err := saveSongs(songsPath); err != nil {
		fmt.Fprintf(os.Stderr, "cannot save %s: %v\n", songsPath, err)
		os.Exit(1)
	}

	fmt.Printf("ok: removed song %d (%s)\n", number, song.Name)
	if c, err := loadConfig(cfgPath); err == nil && c.UnlockMelody.Song == number {
		fmt.Println("warning: this was your unlock-melody song — set a new one with: music settings unlock.song <number>")
	}
	fmt.Println("restart music-service for it to take effect: systemctl restart music-service")
}

func songFileStillReferenced(file string) bool {
	for _, s := range songCatalog {
		for _, t := range s.Tracks {
			if t.File == file {
				return true
			}
		}
	}
	return false
}

// slugify turns a display name into a filesystem-safe filename fragment:
// lowercase, non alphanumeric runs collapsed to a single '-'.
func slugify(name string) string {
	var sb strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			sb.WriteByte('-')
			prevDash = true
		}
	}
	s := strings.Trim(sb.String(), "-")
	if s == "" {
		s = "song"
	}
	return s
}

func songListText() string {
	var sb strings.Builder
	for _, s := range sortedSongs() {
		fmt.Fprintf(&sb, "  %2d  %s\n", s.Number, s.Name)
	}
	return sb.String()
}

const helpTextTemplate = `music-service — Tunu (VESC) music playback controller.

Sends song-select, stop and volume commands to Tunu's app.lisp over CAN
(Music_Control_ID1/2, 0x4E8/0x4E9), and republishes Tunu's Music_Status echo
(0x7E6) as a Redis status hash.

Usage:
  music <command> [args]

Commands:
  play <song number>    Play a song
  stop                  Stop playback
  vol <0-100>           Set master volume
  raw <canid-hex> [hex]  Send a raw CAN frame (canid in hex, e.g. 4E9 32 = volume 50%%; default data 01)
  status                 Show last known playback state (from Tunu's echo)
  songs                   List song numbers and names
  songs add <num> <name> <melody-file> [vol] [decay] [--bass <file> [vol] [decay]]
                           Add your own song (one or two tracks) — see README.md
  songs remove <num>      Remove a song you added (also deletes its track files)
  settings               Show/update unlock-melody settings
  engine                 Engine sound: on [vol] | off | ride [vol] | for <time> [vol] | vol <pct> | demo

Songs:
%s
Flags:
  -h, --help      Show this help
  -v, --version   Show version
  -c, --config    Config file (default /data/music-service/config.json)

Examples:
  music play 2            play La Cucaracha
  music vol 50             half volume
  music stop
  music status
  music songs add 5 "My Song" my-song.mid
  music settings unlock.enabled true
  music settings unlock.song 5
  music settings unlock.duration 10
  music engine ride        engine sound for this ride, off when locked
  music engine for 20m 60  engine sound for 20 minutes at 60%%

Use "music settings --help" for the full settings reference.
`

// helpText is built fresh each run, after loadSongs, since the song list it
// embeds is no longer a compile-time constant — it depends on whatever
// songs.json + songs/ currently holds (defaults, or anything added locally
// via `music songs add`).
var helpText = fmt.Sprintf(helpTextTemplate, "")

func rebuildHelpText() {
	helpText = fmt.Sprintf(helpTextTemplate, songListText())
}

const settingsUsage = `Update the unlock-melody configuration (saved to config.json immediately).

Usage:
  music settings <key> <value>

Settings:
  unlock.enabled <true|false>   Play a melody when the vehicle unlocks
  unlock.song <number>          Which song to play (see "music songs")
  unlock.volume <0-100>         Volume to set first, 0 = leave volume as-is
  unlock.duration <0-300>       Auto-stop after N seconds, 0 = play to completion
  unlock.trigger_field <name>   vehicle hash field to watch (advanced; default "state")
  unlock.trigger_value <value>  Value that counts as the unlock edge (default "parked")
  unlock.trigger_from <value>   Required prior value (default "stand-by"); "" fires on
                                 any transition into trigger_value, even e.g. ending a
                                 ride (ready-to-drive -> parked)

can.interface and redis.addr are boot-time only — edit config.json and
restart the service to change them.

Flags:
  -h, --help   help for settings
`

const engineUsage = `Engine sound: a hum on the motor that follows speed and throttle,
with five virtual gears. Music has priority while a song plays. Needs an
app.lisp with the engine sound (Engine_Sound 0x4EA / Engine_Status 0x7ED).

Usage:
  music engine                     Show mode, volume and Tunu's confirmation
  music engine on [vol]            On until switched off (survives restarts)
  music engine off                 Off
  music engine ride [vol]          On for this ride, off when the scooter is locked
  music engine for <time> [vol]    On for a set time: 10m, 90s, 1h (a plain number
                                   is minutes), then off
  music engine vol <0-100>         Volume only (default 50)
  music engine demo [vol]          Play a ~23 s demo ride at a standstill (idle, full
                                   throttle through the gears, cruise, brake). Same
                                   sound as a real ride; "off" stops it

Tunu is unpowered while locked and starts with the engine sound off, so the
service switches it back on after every unlock while a mode is active.
`
