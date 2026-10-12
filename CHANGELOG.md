# Changelog

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
