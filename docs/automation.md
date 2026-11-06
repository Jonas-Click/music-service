# Automatic music actions

`automation` in config.json provides native event rules with conditions evaluated
by [Expr](https://github.com/expr-lang/expr), the expression library used by
Librescoot event-service. No external scripts or event-service worker are needed.
Manual `music` commands remain direct commands and do not pass through these rules.
A later matching automatic rule may issue another command.
Without rules, existing engine and unlock-melody behavior is unchanged.

## Example

[examples/left-brake-engine.json](../examples/left-brake-engine.json) demonstrates
style rotation, left-brake admission and a dashboard notice at drive start.
Merge its `automation` section into your existing config; retain your own style,
volume, songs and other preferences. Turn off the legacy persistent engine mode
with `music engine off` before using per-start admission rules. Disable overlapping
external rules or automatic melodies if they perform the same actions.

The example advances V8, Moped, Sport, V-Twin, Electric on each observed
`parked` to `ready-to-drive` transition. It starts engine sound only when the
left brake is held at that transition. Releasing the brake afterward leaves
sound running. Parking clears the automatic admission independently of rule
conditions. The next interval needs a new matching rule. Starting the service
already in ready-to-drive does not grant admission or advance the style.

Configuration changes take effect after restarting music-service. The JSON arrays
can also be set with `music settings automation.inputs '<JSON array>'` and
`music settings automation.rules '<JSON array>'`; these are restart-only settings.
Invalid expressions, unknown actions and unknown automation fields are rejected.

## Inputs and observations

```json
"inputs": [
  { "hash": "vehicle", "fields": ["state", "brake:left"] },
  { "hash": "battery:0", "fields": ["state"] }
]
```

Inputs subscribe to exact hash notification channels, such as `vehicle` or
`battery:0`. Notifications must carry the changed field name. `cleared` and
`replaced` refresh all declared fields of that hash. No command or RPC lists
are consumed. The `music` command channel is not available as an input.

A field change emits `<hash>.<field>.changed`, for example
`vehicle.state.changed` or `battery:0.state.changed`. `vehicle.state` is always
available; declare any other fields explicitly. Startup seeds observations
without firing rules, and duplicate values do not fire. Like event-service,
these are notification-based observations, not a complete transition log.

For each notification, declared fields are read into one snapshot. Fields of
the same hash are read together with HMGET; snapshots across hashes are not
atomic. Conditions use only that snapshot, with no Redis reads during evaluation.
Missing or undeclared state values are empty strings. In particular, test a
brake with `== 'on'`, not by assuming that an unknown value means released or held.

## Rules and conditions

A rule has `name`, `on`, optional `when`, and an ordered `actions` array. `on`
accepts an exact topic or trailing prefix wildcard, such as `vehicle.*`.
Rules execute in config order. A false rule condition skips the rule. Actions
can also have `when`; a false action condition skips just that action. An
evaluation error or action dispatch error stops that rule's remaining actions.

Expressions have the same variable names as event-service conditions:

- `topic`, `src` (`adapter`), `from`, `to`
- `data.hash` and `data.field`
- `state("hash", "field")`

Values from Redis are strings. For numeric comparisons, use `float(to)` or
`int(state('battery:0', 'charge'))`. Missing or malformed numeric data produces
an evaluation error instead of allowing the action.

```json
{
  "name": "battery-ready",
  "on": "battery:0.state.changed",
  "when": "to == 'active' and state('vehicle', 'state') == 'parked'",
  "actions": [
    { "do": "engine.style", "style": "electric" },
    { "do": "notify", "title": "Battery ready" }
  ]
}
```

There are at most 16 hashes, 64 selected fields, 32 rules and eight actions per
rule. Input values and expressions are limited to 4096 bytes. Expressions are
compiled once, limited to 256 syntax nodes, and evaluated with a 4096-item VM
memory budget. Configuration text is limited to 64 KiB for the automation section.

## Native actions

| `do` | Parameters | Meaning |
| --- | --- | --- |
| `play` | `song` | Play a catalogue song. |
| `stop` | none | Stop music and cancel pending automatic playback. |
| `volume` | `volume` | Persist music volume, 0-100. |
| `engine.start` | optional `volume` | Enable engine sound for the current ready-to-drive interval only. |
| `engine.off` | optional `volume` | Switch engine mode off. |
| `engine.on` | optional `volume` | Persistent on mode, like the manual command. |
| `engine.ride` | optional `volume` | Arm until the next lock, like the manual command. |
| `engine.for` | `duration`, optional `volume` | Arm for a Go duration such as `30s` or `5m`, between 10s and 24h. |
| `engine.volume` | `volume` | Persist engine volume without changing mode. |
| `engine.style` | `style` | Select v8, moped, sport, vtwin or electric. |
| `engine.cycle` | none | Advance and persist the style without changing mode. |
| `engine.demo` | none | Request the existing demo sequence. |
| `notify` | optional `title`, `body` | Publish a transient dashboard notice. |

Use `engine.start` for conditions that must be checked at every start.
`engine.on`, `engine.ride` and `engine.for` intentionally keep their existing
persistent/session/timer semantics. Parking and loss of power still silence
normal engine sound regardless of rule conditions. Explicit demos retain their
existing behavior and can be stopped with `music stop`.

Engine state changes are applied in action order before their asynchronous
controller confirmations. Put style selection before `engine.start` to use the
new style for the first tone. Music uploads and demo confirmation run outside
the subscriber loop, with one automatic audio operation at a time and no queue.
Additional automatic play/demo requests while busy are rejected, not played
later. Actions that change settings persist them; filter high-rate inputs with
conditions instead of writing a setting on every observation. Manual play/stop cancels pending automatic playback retries.

Notifications use `scootui:notification`. With no title, they show the requested
engine style or sound-off decision, without volume or waiting for a controller
echo. They do not acknowledge actual sound or display presentation, wake the
dashboard, or queue messages while it is disconnected. Delete the notice rule
or give it its own condition to disable notices independently of sound.

Actions do not run executables or send arbitrary raw CAN frames. Debugging and
configuration commands remain manual operations.

## Tests

```sh
go test -race ./...
go vet ./...
python3 -m unittest discover -s tests -v
```

Integration tests require `redis-server` and `redis-cli`. They start an isolated
local Redis and use the log-only CAN sink; they never connect to a scooter.
