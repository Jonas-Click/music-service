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
  the commands below have nothing to talk to.

## Install

On the scooter, if it has internet:

```sh
wget -qO /tmp/music-install.sh \
     https://raw.githubusercontent.com/Jonas-Click/music-service/main/install.sh \
  && sh /tmp/music-install.sh
```

The installer asks a few questions (you can press Enter to accept the
defaults), downloads the program, checks it against the published checksums,
and sets it up to start automatically. Running it again later upgrades or
reconfigures an existing install.

**No internet on the scooter?** Download `music-service`, `install.sh`,
`SHA256SUMS` and `music-completion.bash` from the
[latest release](https://github.com/Jonas-Click/music-service/releases/latest)
on your computer, then:

```sh
ssh root@192.168.7.1 "mkdir -p /data/music-service"
scp music-service install.sh SHA256SUMS music-completion.bash \
    root@192.168.7.1:/data/music-service/
ssh root@192.168.7.1 "sh /data/music-service/install.sh"
```

Everything lives in `/data/music-service/`, so it survives system updates.

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

## Melody on unlock

The scooter can play a song every time you unlock it. It's off by default.

```sh
music settings                       # show the current settings
music settings unlock.enabled true   # turn it on
music settings unlock.song 3         # which song
music settings unlock.volume 80      # volume to use (0 = keep current)
music settings unlock.duration 10    # stop after 10 seconds (0 = play it all)
```

Changes apply immediately — no restart needed.

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
music songs add 5 "My Song" my-song.mid
systemctl restart music-service
```

With a bass line:

```sh
music songs add 5 "My Song" my-song-melody.mid --bass my-song-bass.mid
systemctl restart music-service
```

Optional numbers after each file set its volume and how quickly notes fade
(defaults: melody `1.5 0.92`, bass `0.7 0.92`), for example
`music songs add 5 "My Song" melody.mid 2.0 0.92 --bass bass.mid 0.5 0.92`.

Songs you add stay on your scooter only. Only add music you have the right to
use.

### Remove a song

```sh
music songs remove 5
systemctl restart music-service
```

If it was your unlock song, you'll be reminded to pick another one.

## Troubleshooting

- **Nothing plays / `music status` says "stale":** the motor controller isn't
  answering. Check that it's powered (scooter unlocked) and that its
  `app.lisp` with the music engine is loaded.
- **See what the service is doing:** `journalctl -u music-service -f`
- **Restart it:** `systemctl restart music-service`
- **Right after unlocking**, the controller needs a few seconds to start.
  The unlock melody waits for it automatically; a manual `music play` in that
  moment may need a second try.

<details>
<summary><b>For developers</b></summary>

### How it works

`music-service` is the only process that writes music commands to the CAN bus.
The `music` CLI never touches CAN itself — it publishes commands on the Redis
channel `music`, and the service executes them:

- `play:<song number>`, `stop`, `vol:<0-100>`
- `raw:<canid-hex>:<hexbytes>` (send an arbitrary frame)
- `settings:<key>:<value>`

Songs are not stored on the controller. Before every play, the service uploads
the song's MIDI bytes over CAN, then sends the play command. The controller
keeps only the current song, in two fixed buffers (melody and bass). Axel F is
the one exception: the controller can play it from a package that `vesc_tool`
itself provides, so it's never uploaded. To list it, add
`{"number": 1, "name": "Axel F", "source": "builtin-package"}` to `songs.json`.

Every command is retried until the controller's own status reply confirms it,
so a lost frame doesn't leave the status wrong. The unlock trigger allows
extra time for the controller's boot.

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

Replies from the controller:

| ID | Payload |
|---|---|
| `0x7E6` | playing (0/1), current song, volume % |
| `0x7E7` | song, track, result (0 OK, 1 checksum, 2 length, 3 rejected) |

The bus runs at 125 kbit/s and the controller drops frames that arrive faster
than its script can handle them, so chunks are paced 8 ms apart, and the gap
doubles on retries.

### Redis

Current playback state is kept in the `music` hash (`playing`, `song`, `name`,
`volume`, `updated`). The unlock trigger watches the `vehicle` hash: by
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
  "unlock_melody": {
    "enabled": false,
    "song": 3,
    "volume": 100,
    "duration_seconds": 15,
    "trigger_field": "state",
    "trigger_value": "parked",
    "trigger_from": "stand-by"
  }
}
```

`can` and `redis` need a restart to take effect; everything under
`unlock_melody` can be changed live with `music settings`. The song catalog is
`songs.json` next to it, with the track files in `songs/`.

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
| `config.go` / `settings.go` | config file and live unlock settings |
| `songs.go`, `songs.json`, `songs/` | song catalog and the built-in songs (embedded at build time) |
| `can.go` | frame building, upload framing, checksum |
| `can_linux.go` / `can_dev.go` / `can_other.go` | SocketCAN, development sinks, non-Linux stub |
| `redis.go` | command execution, uploads, unlock trigger, status |
| `resp.go` | minimal Redis client |
| `cli.go` | the `music` command |

</details>

## Credits

Built for the Tunu motor-controller configuration from
[Zanooda/tunu-configs](https://github.com/Zanooda/tunu-configs).

## License

AGPL-3.0 — see [LICENSE](LICENSE).
