# Changelog

## v1.1.0 — 2026-10-30

- `music engine`: engine sound that follows speed and throttle, on until
  switched off, for one ride, or for a set time. Re-applied after every unlock
  (Tunu boots with it off) and only sent while Tunu is powered. Needs an
  `app.lisp` with Engine_Sound (`0x4EA`) / Engine_Status (`0x7ED`).
  Plays only with the kickstand up (ready-to-drive); kickstand down stops it.
  Temporary modes (`ride`, `for`) are never saved, and at every service start
  a powered Tunu is told the wanted state, so an MDB crash or reboot can't
  leave the engine sound running.
- `music engine vol` and `music settings engine.volume` warn above the default
  50 %: the engine sound heats the motor windings, and high volumes can
  physically damage the motor.
- `music engine demo`: a 30-second demo at a standstill (start, ride, stop),
  through the same sound code as a real ride. `music stop` ends it.
- Engine start and stop sounds at kickstand up and down, revving at a
  standstill with the brake pressed, and five styles: `music engine style
  v8|moped|sport|vtwin|electric` (saved in config.json).
- Updates keep your own songs: new built-in songs get the next free number
  after your highest one, removed built-in songs stay removed, and improved
  built-in songs only replace files you haven't changed
  (`songs-defaults.json`). `songs.json` is now written atomically.
- Updates recognise built-in songs you already have under another name or
  number, so they aren't added twice (by teal-bauer).
- New built-in song: robot car for justice (riff and fanfare, melody plus
  bass), by teal-bauer. Number 5 on a new install, the next free number on an
  existing one.
- New built-in songs: Space Journey (melody plus bass) and Space Journey
  Fanfare, by teal-bauer (credits in `MEDIA.md`). Numbers 6 and 7 on a new
  install, the next free numbers on an existing one.
- All settings in `config.json`, one load and one save path: the music volume
  (`music vol`) is now saved too (`music.volume`) and applied again after every
  unlock; `music settings` shows and changes `music.volume`,
  `engine.enabled`, `engine.volume` and `engine.style` next to the unlock
  melody. `unlock.volume 0` now means "the music volume". Every setting in
  `config.json` can be changed with `music settings <key> <value>`, including
  `status.stale_seconds`, `can.interface` and `redis.addr` (the last two after
  a restart); keys, checks, help and shell completion come from one table.
- `music raw:<id>:<hex>` sends the given payload again; it used to send only
  `01` (by teal-bauer).
- `music` is now a real command in `/data/bin/music` instead of a shell
  alias, linked from `/usr/local/bin/music`, so it also works where no
  profile is loaded (the Sunshine web shell, `sh -c`, scripts). The link is
  restored on every boot after a system update. The installer removes the
  old alias.
- The installer no longer hangs on a bad connection: downloads give up after
  20 seconds without data (at most 3 tries), and it says what it's fetching.
- Safer installer (by teal-bauer): it downloads and checks every release file
  against `SHA256SUMS` before changing anything, keeps `config.json` and your
  songs, backs up the old install to `/data/music-service-backups/` (the three
  newest backups are kept), and puts it back if the new service doesn't start.
  No more questions: settings are changed afterwards with `music settings`.
  Without internet, copy the release files to the scooter and run
  `install.sh --offline <dir>`. The service, `music` command and shell
  completion are restored on every boot after a system update. Releases now
  also ship `music-service.service` and `config.json.example`, which the
  installer needs.

## v1.0.0 — 2026-10-12

Initial release.

- `music-service` daemon: the single writer to the CAN interface, driven by
  the `music` Redis channel (`play:<n>`, `stop`, `vol:<pct>`,
  `raw:<id>:<hex>`, `settings:<key>:<value>`).
- Songs are streamed to the motor controller over CAN right before they play
  (upload frames `0x4EC`-`0x4EE`, confirmed on `0x7E7`), so the controller
  only ever holds the current song. Uploads are paced for the 125 kbit/s bus
  and retried with a growing gap if the controller misses frames.
- Play, stop and volume (`0x4E8`/`0x4E9`) are retried until the controller's
  own status reply (`0x7E6`) confirms them; that reply is republished as the
  `music` Redis hash and read by `music status`.
- Built-in songs: La Cucaracha, Korobeiniki (melody plus an original bass
  line), Happy Birthday to You. Add or remove your own with
  `music songs add` / `music songs remove`; number 1 is reserved for Axel F,
  which the controller can play from a `vesc_tool` package.
- Optional unlock melody: plays a chosen song when the vehicle goes from
  `stand-by` to `parked`, at a chosen volume, optionally stopping after a
  set time, and waits for the controller to finish booting.
- `music` CLI (`play`, `stop`, `vol`, `raw`, `status`, `songs`, `settings`),
  systemd unit, and an installer/uninstaller that installs to `/data` and
  re-syncs the unit, alias and completion on every boot so they survive
  system updates.
- Releases are built by CI from a clean runner on every version tag.
