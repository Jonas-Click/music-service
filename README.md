# music-service

Play melodies through the motor of your scooter — on demand from the command
line, or automatically when you unlock it.

`music-service` runs on the scooter's main computer (the MDB) and talks to a
VESC-based **Tunu** motor controller over the scooter's CAN bus. The
controller turns the notes into sound by making the motor hum them.

## What you need

- A scooter whose MDB you can reach as `root` over SSH (for example over USB at
  `192.168.7.1`).
- A Tunu motor controller running an `app.lisp` that includes the **music
  engine** (the part that receives songs over CAN and plays them). Without it,
  the commands below have nothing to talk to. The engine sound
  (`music engine`) also needs the engine-sound part of `app.lisp`.

## Install

On the scooter, if it has internet:

```sh
installer=$(mktemp /tmp/music-install.XXXXXX) &&
  wget -T 20 -qO "$installer" https://github.com/Jonas-Click/music-service/releases/latest/download/install.sh &&
  sh "$installer" && rm -f "$installer"
```

The installer downloads the matching release, verifies all files against its checksums before changing the installation, and starts the service. Existing configuration, songs and numbers are kept, including when the old executable is in `/data/bin`. Backups are retained in `/data/music-service-backups/`; if the new service fails to start, the installer restores the previous executable and setup.

**No internet on the scooter?** Download `music-service`, `install.sh`, `SHA256SUMS`, `music-completion.bash`, `music-service.service` and `config.json.example` from the [latest release](https://github.com/Jonas-Click/music-service/releases/latest) onto your computer, then stage them separately:

```sh
ssh root@192.168.7.1 "mkdir -p /data/music-release"
scp music-service install.sh SHA256SUMS music-completion.bash \
    music-service.service config.json.example root@192.168.7.1:/data/music-release/
ssh root@192.168.7.1 "sh /data/music-release/install.sh --offline /data/music-release"
```

Offline installation makes no network requests and verifies the same checksums. The installer updates the MDB service, not the VESC app or firmware. The service and configuration live in `/data/music-service/`, with the `music` command in `/data/bin/`.

**Uninstall:**

```sh
sh /data/music-service/install.sh --uninstall
```

## Everyday use

```sh
music songs          # list the songs you can play
music play 2         # play song number 2
music stop
music vol 50         # volume 0-100
music status         # what's playing right now
music help           # all commands
```

`music` is a real command in `/data/bin`, not an alias, with a link in
`/usr/local/bin` so it also works in shells that load no profile, like the
Sunshine web shell. System updates remove that link; it comes back on the
next boot.

## Settings

Every setting is kept in one file and changed the same way:

```sh
music settings                       # show all settings and their values
music settings music.volume 70       # change one
music settings --help                # what each one does
```

`music vol` and `music engine ...` are shortcuts for the same settings.
Changes apply immediately, except `can.interface`, `redis.addr` and
`unlock.trigger_field`, which are saved now and used after
`systemctl restart music-service`.

## Melody on unlock

The scooter can play a song every time you unlock it. It's off by default.

```sh
music settings                       # show the current settings
music settings unlock.enabled true   # turn it on
music settings unlock.song 3         # which song
music settings unlock.volume 80      # volume to use (0 = the music volume)
music settings unlock.duration 10    # stop after 10 seconds (0 = play it all)
```

Changes apply immediately — no restart needed.

## Engine sound

The motor can also sound like an engine while you ride: the pitch follows
your speed through virtual gears and gets louder when you accelerate. Once
switched on, the engine starts (with a start sound) when you flip the
kickstand up and stops (with a stop sound) when you put it down. Standing
still with the brake pressed, the throttle revs it. It needs an `app.lisp`
with the engine sound. Music has priority while a song plays.

Styles: `v8` (default), `moped` (two-stroke, kick start), `sport` (sport
bike), `vtwin` (cruiser) and `electric` (sci-fi whine). All are equally
loud.

```sh
music engine ride        # on for this ride, off when you lock the scooter
music engine for 20m     # on for 20 minutes (also 90s, 1h)
music engine on          # on until you switch it off
music engine off
music engine vol 60      # volume 0-100 (default 50)
music engine style moped # sound style
music engine demo        # hear a 30-second demo while standing still
                         # (music stop ends it)
music engine             # show the current mode
```

## Songs

These songs come built in:

| # | Song |
|---|------|
| 2 | La Cucaracha |
| 3 | Korobeiniki |
| 4 | Happy Birthday to You |
| 5 | robot car for justice (riff + fanfare) |

Number 1 is reserved and can't be used for your own songs.

### Add your own

You need your song as a MIDI file with a single melody line (and optionally a
second file with a bass line). Each file must be at most 2048 bytes — short,
simple tunes work best, since the motor plays at most a few notes at once.

```sh
music songs add 6 "My Song" my-song.mid
systemctl restart music-service
```

With a bass line:

```sh
music songs add 6 "My Song" my-song-melody.mid --bass my-song-bass.mid
systemctl restart music-service
```

Optional numbers after each file set its volume and how quickly notes fade
(defaults: melody `1.5 0.92`, bass `0.7 0.92`), for example
`music songs add 6 "My Song" melody.mid 2.0 0.92 --bass bass.mid 0.5 0.92`.

Songs you add stay on your scooter only. Only add music you have the right to
use.

### Remove a song

```sh
music songs remove 6
systemctl restart music-service
```

If it was your unlock song, you'll be reminded to pick another one.

### Updates and your songs

Updates never touch your own songs or their numbers. New built-in songs in a
later release get the next free number after your highest one (with your own
songs at 5 and 6, new ones become 7, 8, ...). A built-in song you removed stays
removed, and an improved version of a built-in song only replaces the old one
if you haven't changed its file. What was added is in the service log
(`journalctl -u music-service`).

## Troubleshooting

- **Nothing plays / `music status` says "stale":** the motor controller isn't
  answering. Check that it's powered (scooter unlocked) and that its
  `app.lisp` with the music engine is loaded.
- **See what the service is doing:** `journalctl -u music-service -f`
- **Restart it:** `systemctl restart music-service`
- **Right after unlocking**, the controller needs a few seconds to start.
  The unlock melody waits for it automatically; a manual `music play` in that
  moment may need a second try.
- **Engine sound silent:** it only plays with the kickstand up. `music engine`
  shows the mode and whether the controller confirmed it; "no Engine_Status
  echo yet" means the controller's `app.lisp` has no engine sound, or the
  scooter is locked.

<details>
<summary><b>For developers</b></summary>

### How it works

`music-service` is the only process that writes music commands to the CAN bus.
The `music` CLI never touches CAN itself — it publishes commands on the Redis
channel `music`, and the service executes them:

- `play:<song number>`, `stop`, `vol:<0-100>`
- `raw:<canid-hex>:<hexbytes>` (send an arbitrary frame)
- `settings:<key>:<value>`
- `engine:on[:vol]`, `engine:off`, `engine:ride[:vol]`,
  `engine:for:<seconds>[:vol]`, `engine:vol:<0-100>`, `engine:demo[:vol]`,
  `engine:style:<name>`

Songs are not stored on the controller. Before every play, the service uploads
the song's MIDI bytes over CAN, then sends the play command. The controller
keeps only the current song, in two fixed buffers (melody and bass). Axel F is
the one exception: the controller can play it from a package that `vesc_tool`
itself provides, so it's never uploaded. To list it, add
`{"number": 1, "name": "Axel F", "source": "builtin-package"}` to `songs.json`.

Every command is retried until the controller's own status reply confirms it,
so a lost frame doesn't leave the status wrong. The unlock trigger allows
extra time for the controller's boot.

The engine sound is a mode the service keeps (`off`, `on`, `ride`, `timed`).
While a mode is active it is switched on when the vehicle `state` goes to
`ready-to-drive` (kickstand up) and off when it goes back to `parked`; `ride`
ends when the controller loses power (locking), `timed` at its deadline. The
controller starts with the engine sound off at every power-up, so the service
sends it again, allowing for the boot time. Nothing is sent while the
controller is unpowered: unacknowledged frames would push the CAN controller
towards bus-off. Only `on` and the volume are saved in `config.json`.

### CAN frames

All standard 11-bit frames, in the range the controller's firmware passes to
its Lisp scripts:

| ID | Payload | Meaning |
|---|---|---|
| `0x4E8` | `0` stop, `N` play song `N` | play / stop |
| `0x4E9` | `0-100` | master volume |
| `0x4EC` | song, track, length (u16 LE), volume×10, decay×100 | upload: begin a track |
| `0x4ED` | sequence, up to 7 data bytes | upload: chunk |
| `0x4EE` | song, track, checksum (sum of bytes mod 256) | upload: end |
| `0x4EA` | `0` off / `1` on / `2` demo ride, volume `0-100`, style `0-4` | engine sound |

Replies from the controller:

| ID | Payload |
|---|---|
| `0x7E6` | playing (0/1), current song, volume % |
| `0x7E7` | song, track, result (0 OK, 1 checksum, 2 length, 3 rejected) |
| `0x7ED` | engine sound on (0/1), volume %, demo playing (0/1), style |

The bus runs at 125 kbit/s and the controller drops frames that arrive faster
than its script can handle them, so chunks are paced 8 ms apart, and the gap
doubles on retries.

### Redis

Current playback state is kept in the `music` hash (`playing`, `song`, `name`,
`volume`, `updated`). The engine sound adds the wanted state (`engine_mode`,
`engine_until`, `engine_volume_set`, `engine_style_set`, `engine_sounding`)
and the controller's confirmation (`engine_on`, `engine_volume`,
`engine_style`, `engine_demo`, `engine_updated`).
The engine sound follows the `vehicle` hash's `state` field. The unlock trigger watches the `vehicle` hash: by
default it fires when `state` changes from `stand-by` to `parked`. Ending a
ride also leads to `parked`, which is why the previous value is checked too.
All three are configurable (`unlock.trigger_field`, `unlock.trigger_value`,
`unlock.trigger_from`).

### Configuration file

`/data/music-service/config.json`:

```json
{
  "can": { "interface": "can0" },
  "redis": { "addr": "127.0.0.1:6379" },
  "status": { "stale_seconds": 5 },
  "music": { "volume": 100 },
  "unlock_melody": {
    "enabled": false,
    "song": 3,
    "volume": 100,
    "duration_seconds": 15,
    "trigger_field": "state",
    "trigger_value": "parked",
    "trigger_from": "stand-by"
  },
  "engine_sound": {
    "enabled": false,
    "volume": 50,
    "style": "v8"
  }
}
```

All settings live in this one file, loaded with defaults for anything
missing and saved atomically (temp file, fsync, rename) on every change. Each
one has a `music settings` key (`unlock_melody` is `unlock.*`,
`engine_sound` is `engine.*`, `duration_seconds` is `unlock.duration`); the
keys, their checks and their help come from one table, `settings_registry.go`.
`can.interface`, `redis.addr` and `unlock.trigger_field` are used after a
restart, everything else at once. The music volume is applied again after every unlock, since
the controller starts at 100 % when it powers up; an unlock melody with its
own volume plays at that and the music volume comes back afterwards. The song catalog is
`songs.json` next to it, with the track files in `songs/`.

Built-in songs carry a `"default"` key in `songs.json` (and `"since"`, the
release that first shipped them, in the embedded copy). At every start the
service merges the embedded ones in: a key it hasn't offered before is added
under the next free number, existing entries are never renumbered or removed,
and a track file is only replaced if its checksum still matches what was
installed. What has been offered, with those checksums, is kept in
`songs-defaults.json`. To ship a new built-in song, add an entry with a new
key and `"since"` to the embedded `songs.json` and its files to `songs/`; the
number there is only used on a fresh install.

### Build

Go 1.21 or later, no external dependencies.

```sh
make build   # for your computer
make cross   # ARMv7 binary for the scooter
make vet
```

It builds on any OS. Without CAN hardware, run it with a stand-in sink:

```sh
go run . --device log:    # print frames instead of sending them
go run . --device null:   # discard them
```

Releases are built by CI when a version tag is pushed.

### Source layout

| File | |
|---|---|
| `main.go` | startup, arguments, signals |
| `config.go` / `settings.go` | config file, its defaults and the one save path; live settings |
| `settings_registry.go` | every setting: key, check, how it applies, help |
| `songs.go`, `songs.json`, `songs/` | song catalog and the built-in songs (embedded at build time) |
| `songs_defaults.go` | merging new and improved built-in songs into an existing catalog |
| `can.go` | frame building, upload framing, checksum |
| `can_linux.go` / `can_dev.go` / `can_other.go` | SocketCAN, development sinks, non-Linux stub |
| `redis.go` | command execution, uploads, unlock trigger, status |
| `engine.go` | engine-sound modes, kickstand and unlock handling, demo |
| `resp.go` | minimal Redis client |
| `cli.go` | the `music` command |

</details>

## Credits

Built for the Tunu motor-controller configuration from
[Zanooda/tunu-configs](https://github.com/Zanooda/tunu-configs).

## License

AGPL-3.0 — see [LICENSE](LICENSE).
