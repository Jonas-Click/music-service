// music-service drives Tunu (VESC) music playback on electric scooters,
// from the MDB side.
//
// It sends song-select, stop and volume commands to Tunu's app.lisp over
// plain standard CAN frames (Music_Control_ID1/2, 0x4E8/0x4E9), republishes
// Tunu's Music_Status echo (0x7E6) as a Redis status hash, and can play a
// configurable melody when the vehicle unlocks.
//
// This file holds startup only. The rest of the service:
//
//	config.go     on-disk config, defaults, atomic save
//	settings.go   the runtime-mutable subset of the config (unlock melody), behind a lock
//	songs.go      the song number -> name table
//	can.go        command framing (play/stop/vol) + the canSink interface
//	can_linux.go  the real SocketCAN (PF_CAN/CAN_RAW) transport
//	can_dev.go    log:/null: sinks for development without hardware
//	can_other.go  build stub for non-Linux hosts
//	redis.go      the channel watcher: executes commands, drives the unlock trigger, republishes status
//	resp.go       a minimal Redis client
//	cli.go        `music play|stop|vol|raw|status|settings`, help text
//
// Two goroutines run in the daemon: redisWatcher, which is the only writer
// to the CAN sink, and canReader, which is the only reader. Runtime-mutable
// state (settings.go) is the only thing both sides touch, and it's behind a
// lock.
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

// version is injected at build time via -ldflags "-X main.version=<tag>".
var version = "dev"

var quit = make(chan struct{})

func initSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		log.Printf("signal received, shutting down")
		close(quit)
	}()
}

func main() {
	log.SetFlags(0) // timestamps come from journald

	cfgPath = defaultConfigPath
	args := os.Args[1:]

	if len(args) > 0 {
		switch args[0] {
		case "ctl":
			runCtl(args[1:])
			return
		case "play", "stop", "vol", "raw", "status", "songs", "settings", "engine", "help", "--help", "-h", "--version", "-v", "version":
			// Bare invocation (no "ctl") also works as the CLI — matches
			// music.sh's old `music.sh play|stop|vol|raw` usage so existing
			// muscle memory and scripts keep working.
			runCtl(args)
			return
		}
	}

	deviceOverride := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config", "-c":
			if i+1 < len(args) {
				cfgPath = args[i+1]
				i++
			}
		case "--device", "-d":
			if i+1 < len(args) {
				deviceOverride = args[i+1]
				i++
			}
		}
	}

	var err error
	cfg, err = loadConfig(cfgPath)
	if err != nil {
		log.Printf("error: %v", err)
		os.Exit(1)
	}
	live.init(cfg)
	engineInit(cfg)

	songsPath := filepath.Join(filepath.Dir(cfgPath), "songs.json")
	syncDefaultSongsAtStart(songsPath)
	if err := loadSongs(songsPath); err != nil {
		log.Printf("error: %v", err)
		os.Exit(1)
	}

	device := cfg.CAN.Interface
	if deviceOverride != "" {
		device = deviceOverride
	}

	sink, err := openCANSink(device)
	if err != nil {
		log.Printf("error: CAN device %s: %v", device, err)
		os.Exit(1)
	}
	defer sink.close()

	log.Printf("music-service %s starting  device=%s redis=%s", version, sink.describe(), cfg.Redis.Addr)

	initSignal()
	go canReader(sink)
	redisWatcher(sink)

	fmt.Println("music-service exiting")
}
