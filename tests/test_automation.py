import json
import subprocess
import time
from engine_fixture import EngineFixture

EDGE = "from == 'parked' and to == 'ready-to-drive'"


def rule(name, condition, actions, on="vehicle.state.changed"):
    return {"name": name, "on": on, "when": condition, "actions": actions}


class AutomationIntegration(EngineFixture):
    def setUp(self):
        super().setUp()
        config = json.loads(self.config.read_text())
        config["engine_sound"]["enabled"] = False
        config["automation"] = {
            "inputs": [{"hash": "vehicle", "fields": ["state", "brake:left"]}],
            "rules": [rule("style", EDGE, [{"do": "engine.cycle"}]),
                      rule("admission", EDGE + " and state('vehicle', 'brake:left') == 'on'", [{"do": "engine.start"}]),
                      rule("notice", EDGE, [{"do": "notify"}])]}
        self.config.write_text(json.dumps(config))

    def subscribe(self):
        self.messages = self.root / "notifications"
        with self.messages.open("w") as output:
            process = subprocess.Popen(["redis-cli", "-p", str(self.port), "--raw", "SUBSCRIBE", "scootui:notification"],
                                       stdout=output, stderr=subprocess.DEVNULL)
        self.addCleanup(self.terminate, process)
        self.wait(lambda: "scootui:notification" in self.messages.read_text())

    def notifications(self):
        return [json.loads(line) for line in self.messages.read_text().splitlines() if line.startswith("{")]

    def test_conditional_start_is_per_interval_and_style_precedes_first_frame(self):
        self.start()
        self.subscribe()
        self.transition("ready-to-drive", "off")
        self.wait(lambda: len(self.notifications()) == 1)
        self.assertEqual(self.notifications()[0]["title"], "Engine sound off")
        self.assertEqual(self.on_frames(), 0)
        self.redis("HSET", "vehicle", "brake:left", "on")
        self.redis("PUBLISH", "vehicle", "brake:left")
        self.redis("PUBLISH", "vehicle", "state")
        time.sleep(0.1)
        self.assertFalse(self.sounding())
        self.transition("parked", "on")
        start = time.monotonic()
        self.transition("ready-to-drive", "on")
        self.wait(lambda: len(self.notifications()) == 2)
        self.assertLess(time.monotonic() - start, 2)
        self.assertEqual(self.notifications()[1]["title"], "Engine sound: Sport")
        self.wait(lambda: "id=0x4EA data=01 32 02" in self.log.read_text())
        self.redis("HSET", "vehicle", "brake:left", "off")
        self.redis("PUBLISH", "vehicle", "brake:left")
        time.sleep(0.1)
        self.assertTrue(self.sounding())
        self.transition("parked")
        self.assertFalse(self.sounding())
        self.assertEqual(self.redis("HGET", "music", "engine_mode"), "off")
        self.transition("ready-to-drive")
        self.wait(lambda: len(self.notifications()) == 3)
        self.assertEqual(self.notifications()[2]["title"], "Engine sound off")
        self.assertFalse(self.sounding())

    def test_manual_engine_command_is_not_gated(self):
        self.start()
        self.transition("ready-to-drive")
        self.assertFalse(self.sounding())
        self.redis("PUBLISH", "music", "engine:on")
        self.wait(lambda: self.sounding())
        self.wait(lambda: self.on_frames() > 0)
        self.redis("PUBLISH", "music", "engine:off")
        self.wait(lambda: not self.sounding())

    def test_restart_does_not_replay_events_or_restore_interval(self):
        self.start()
        self.transition("ready-to-drive", "on")
        self.wait(lambda: self.on_frames() > 0)
        self.stop()
        count = self.on_frames()
        style = json.loads(self.config.read_text())["engine_sound"]["style"]
        self.start()
        self.assertFalse(self.sounding())
        self.assertEqual(self.on_frames(), count)
        self.assertEqual(json.loads(self.config.read_text())["engine_sound"]["style"], style)

    def test_declared_other_hash_and_per_action_condition(self):
        config = json.loads(self.config.read_text())
        config["automation"]["inputs"].append({"hash": "battery:0", "fields": ["state"]})
        config["automation"]["rules"] = [rule("battery", "to == 'active'", [
            {"do": "engine.style", "style": "electric", "when": "false"},
            {"do": "engine.volume", "volume": 25, "when": "state('vehicle', 'state') == 'parked'"},
            {"do": "notify", "title": "Battery ready"}], on="battery:0.state.changed")]
        self.config.write_text(json.dumps(config))
        self.redis("HSET", "battery:0", "state", "idle")
        self.start()
        self.subscribe()
        self.redis("HSET", "battery:0", "state", "active")
        self.redis("PUBLISH", "battery:0", "state")
        self.wait(lambda: len(self.notifications()) == 1)
        config = json.loads(self.config.read_text())
        self.assertEqual(config["engine_sound"]["volume"], 25)
        self.assertEqual(config["engine_sound"]["style"], "v8")
        self.assertEqual(self.notifications()[0]["title"], "Battery ready")
        self.assertEqual(self.on_frames(), 0)

    def test_companion_read_does_not_consume_state_edge(self):
        self.start()
        self.redis("HSET", "vehicle", "state", "ready-to-drive", "brake:left", "on")
        self.redis("PUBLISH", "vehicle", "brake:left")
        time.sleep(0.1)
        self.redis("PUBLISH", "vehicle", "state")
        self.wait(lambda: self.sounding())
        self.wait(lambda: "id=0x4EA data=01 32 01" in self.log.read_text())

    def test_rules_without_brake_declaration_do_not_infer_a_value(self):
        config = json.loads(self.config.read_text())
        config["automation"]["inputs"][0]["fields"] = ["state"]
        self.config.write_text(json.dumps(config))
        self.start()
        self.transition("ready-to-drive", "on")
        self.assertFalse(self.sounding())
        self.assertEqual(self.on_frames(), 0)

    def test_invalid_snapshot_cannot_prevent_parking_stop(self):
        self.start()
        self.transition("ready-to-drive", "on")
        self.wait(lambda: self.sounding())
        self.redis("HSET", "vehicle", "state", "parked", "brake:left", "x" * 4097)
        self.redis("PUBLISH", "vehicle", "state")
        self.wait(lambda: self.redis("HGET", "music", "engine_vehicle_state") == "parked")
        self.assertFalse(self.sounding())
        self.assertIn("exceeds 4096 bytes", self.log.read_text())

    def test_background_play_drops_overlap_and_manual_stop_cancels_retries(self):
        config = json.loads(self.config.read_text())
        config["automation"]["inputs"].append({"hash": "test-input", "fields": ["counter"]})
        config["automation"]["rules"] = [rule("play", "true", [{"do": "play", "song": 1}], on="test-input.counter.changed")]
        self.config.write_text(json.dumps(config))
        (self.root / "songs.json").write_text(json.dumps([{"number": 1, "name": "Axel F", "source": "builtin-package"}]))
        self.redis("HSET", "test-input", "counter", "0")
        self.start()
        self.redis("HSET", "test-input", "counter", "1")
        self.redis("PUBLISH", "test-input", "counter")
        self.wait(lambda: "id=0x4E8 data=01" in self.log.read_text())
        self.redis("HSET", "test-input", "counter", "2")
        self.redis("PUBLISH", "test-input", "counter")
        self.wait(lambda: "automatic audio action already running" in self.log.read_text())
        self.redis("PUBLISH", "music", "stop")
        self.wait(lambda: "id=0x4E8 data=00" in self.log.read_text())
        count = self.log.read_text().count("id=0x4E8 data=01")
        time.sleep(2)
        self.assertEqual(self.log.read_text().count("id=0x4E8 data=01"), count)

    def test_automation_edits_require_restart(self):
        self.start()
        payload = json.dumps([rule("new", EDGE, [{"do": "engine.start"}])], separators=(",", ":"))
        self.redis("PUBLISH", "music", "settings:automation.rules:" + payload)
        self.wait(lambda: json.loads(self.config.read_text())["automation"]["rules"][0]["name"] == "new")
        self.transition("ready-to-drive", "off")
        self.assertFalse(self.sounding())
        self.stop()
        self.redis("HSET", "vehicle", "state", "parked")
        self.start()
        self.transition("ready-to-drive", "off")
        self.wait(lambda: self.sounding())
