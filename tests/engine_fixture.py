"""Isolated service fixture: temporary Redis and log-only CAN, never a vehicle."""
import json
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import unittest

AVAILABLE = all(shutil.which(tool) for tool in ("go", "redis-server", "redis-cli"))


class EngineFixture(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not AVAILABLE:
            raise unittest.SkipTest("requires Go, redis-server and redis-cli")
        cls.build_dir = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.build_dir.cleanup)
        cls.binary = Path(cls.build_dir.name) / "music-service"
        subprocess.run(["go", "build", "-race", "-o", str(cls.binary), "."],
                       cwd=Path(__file__).resolve().parents[1], check=True, timeout=120)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            self.port = sock.getsockname()[1]
        self.server = subprocess.Popen([
            "redis-server", "--bind", "127.0.0.1", "--port", str(self.port),
            "--save", "", "--appendonly", "no", "--dir", str(self.root)],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        self.addCleanup(self.terminate, self.server)
        self.wait(lambda: self.redis("PING") == "PONG")
        self.redis("HSET", "vehicle", "state", "parked", "brake:left", "off")
        self.config = self.root / "config.json"
        self.config.write_text(json.dumps({"redis": {"addr": f"127.0.0.1:{self.port}"},
                                         "engine_sound": {"enabled": True, "volume": 50, "style": "v8"}}))
        self.log = self.root / "service.log"
        self.service = None
        self.addCleanup(self.stop)

    def redis(self, *args):
        return subprocess.check_output(["redis-cli", "-p", str(self.port), "--raw", *args],
                                       text=True, stderr=subprocess.DEVNULL, timeout=5).strip()

    def wait(self, predicate, timeout=5):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                if predicate():
                    return
            except (subprocess.CalledProcessError, FileNotFoundError):
                pass
            time.sleep(0.02)
        self.fail("condition timed out")

    @staticmethod
    def terminate(process):
        if process.poll() is None:
            process.terminate()
        process.wait(timeout=8)

    def stop(self):
        if self.service is not None:
            self.terminate(self.service)
            self.assertNotIn("WARNING: DATA RACE", self.log.read_text())
            self.assertEqual(self.service.returncode, 0)
            self.service = None

    def start(self):
        connections = self.log.read_text().count("redis connected") if self.log.exists() else 0
        with self.log.open("a") as log:
            self.service = subprocess.Popen([str(self.binary), "--config", str(self.config), "--device", "log:"],
                                            stdout=log, stderr=log)
        self.wait(lambda: self.log.read_text().count("redis connected") > connections)

    def transition(self, state, brake="off"):
        self.redis("HSET", "vehicle", "state", state, "brake:left", brake)
        self.redis("PUBLISH", "vehicle", "state")
        self.wait(lambda: self.redis("HGET", "music", "engine_vehicle_state") == state)

    def sounding(self):
        return self.redis("HGET", "music", "engine_sounding") == "true"

    def on_frames(self):
        return self.log.read_text().count("id=0x4EA data=01")
