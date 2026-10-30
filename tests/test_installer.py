import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[1]
ASSETS = ('music-service', 'install.sh', 'music-completion.bash', 'music-service.service', 'config.json.example')


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.root = self.base / 'root'
        self.bundle = self.base / 'release'
        self.commands = self.base / 'commands'
        for path in (self.root, self.bundle, self.commands):
            path.mkdir()
        for name in ASSETS[1:]:
            shutil.copyfile(REPO / name, self.bundle / name)
        binary = self.bundle / 'music-service'
        binary.write_text('#!/bin/sh\ncase "$1" in --version) echo v-test;; *) printf "%s\\n" "$*";; esac\n')
        binary.chmod(0o755)
        self.sums()
        systemctl = self.commands / 'systemctl'
        systemctl.write_text('''#!/bin/sh
set -e
[ "$1" != --no-block ] || shift
printf '%s\\n' "$*" >> "$MUSIC_ROOT/systemctl.log"
case "$1" in
is-active) test -f "$MUSIC_ROOT/active" ;;
is-enabled) test -f "$MUSIC_ROOT/enabled" ;;
stop) rm -f "$MUSIC_ROOT/active" ;;
disable) rm -f "$MUSIC_ROOT/enabled" ;;
enable) touch "$MUSIC_ROOT/enabled" ;;
start|restart)
 if [ -f "$MUSIC_ROOT/fail-start" ]; then rm "$MUSIC_ROOT/fail-start"; exit 1; fi
 touch "$MUSIC_ROOT/active" ;;
*) : ;;
esac
''')
        systemctl.chmod(0o755)
        sleep = self.commands / 'sleep'
        sleep.write_text('#!/bin/sh\nexit 0\n')
        sleep.chmod(0o755)
        self.env = dict(os.environ, MUSIC_ROOT=str(self.root), PATH=str(self.commands) + ':' + os.environ['PATH'])

    def sums(self):
        (self.bundle / 'SHA256SUMS').write_text(''.join(
            hashlib.sha256((self.bundle / name).read_bytes()).hexdigest() + '  ' + name + '\n'
            for name in ASSETS))

    def run_install(self, *args, success=True):
        command = ['sh', str(REPO / 'install.sh'), *(args or ('--offline', str(self.bundle)))]
        result = subprocess.run(command, env=self.env, text=True, capture_output=True, timeout=15)
        if success:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def existing(self):
        install = self.root / 'data/music-service'
        (install / 'songs').mkdir(parents=True)
        config = b'{"engine_sound":{"enabled":true,"style":"electric","volume":50},"unlock_melody":{"song":9,"duration_seconds":0}}\n'
        (install / 'config.json').write_bytes(config)
        (install / 'songs.json').write_text('[{"number":9,"name":"custom","tracks":[]}]\n')
        (install / 'songs/custom.bin').write_bytes(b'private-user-data')
        (self.root / 'data/onboot.sh').write_text('#!/bin/sh\necho keep-this-hook\n')
        (self.root / 'data/bin').mkdir()
        (self.root / 'data/bin/music-service').write_text('old executable')
        (self.root / 'active').touch()
        (self.root / 'enabled').touch()
        return install, config

    def test_fresh_install_and_real_nonlogin_command(self):
        self.run_install()
        install = self.root / 'data/music-service'
        self.assertEqual((install / 'config.json').read_bytes(), (self.bundle / 'config.json.example').read_bytes())
        result = subprocess.run([str(self.root / 'usr/local/bin/music'), 'engine', 'style', 'electric'], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), 'ctl engine style electric')
        self.assertTrue((self.root / 'data/bin/music-service').is_symlink())
        self.assertTrue((install / 'install.sh').exists())

    def test_upgrade_detects_config_without_old_canonical_binary(self):
        install, config = self.existing()
        original_catalogue = (install / 'songs.json').read_bytes()
        self.run_install()
        self.assertEqual((install / 'config.json').read_bytes(), config)
        self.assertEqual((install / 'songs.json').read_bytes(), original_catalogue)
        self.assertEqual((install / 'songs/custom.bin').read_bytes(), b'private-user-data')
        self.assertEqual((self.root / 'data/bin/music-service').read_bytes(), (self.bundle / 'music-service').read_bytes())
        self.run_install()
        self.assertEqual((self.root / 'data/onboot.sh').read_text().count('# >>> music-service >>>'), 1)
        self.assertIn('echo keep-this-hook', (self.root / 'data/onboot.sh').read_text())
        self.assertEqual((install / 'config.json').read_bytes(), config)
        self.assertEqual((self.root / 'data/music-service-backups').stat().st_mode & 0o777, 0o700)

    def test_only_the_three_newest_backups_are_kept(self):
        self.existing()
        backups = self.root / 'data/music-service-backups'
        backups.mkdir(parents=True)
        (backups / 'manual').mkdir()
        made = []
        for _ in range(5):
            self.run_install()
            made += [path for path in backups.glob('install-*') if path not in made]
        self.assertEqual(sorted(backups.glob('install-*')), sorted(made[-3:]))
        self.assertTrue((backups / 'manual').exists())
        (self.root / 'fail-start').touch()
        self.run_install(success=False)
        self.assertEqual(len(list(backups.glob('install-*'))), 4)

    def test_custom_unit_options_survive(self):
        self.existing()
        unit = self.root / 'etc/systemd/system/music-service.service'
        unit.parent.mkdir(parents=True)
        custom = '[Service]\nExecStart=/data/bin/music-service --device can0\nRestartSec=17\n'
        unit.write_text(custom)
        self.run_install()
        self.assertIn('RestartSec=17', unit.read_text())
        self.assertIn('--device can0', unit.read_text())

    def test_boot_restores_ephemeral_files_without_restart_loop(self):
        self.existing()
        self.run_install()
        paths = ('etc/systemd/system/music-service.service', 'etc/profile.d/music-path.sh',
                 'etc/bash_completion.d/music', 'usr/local/bin/music')
        for path in paths:
            (self.root / path).unlink()
        result = subprocess.run(['sh', str(self.root / 'data/onboot.sh')], env=self.env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        for path in paths:
            self.assertTrue((self.root / path).exists(), path)
        self.assertNotIn('Wants=librescoot-onboot', (self.root / paths[0]).read_text())

    def test_checksum_failure_and_missing_manifest_entry_change_nothing(self):
        install, config = self.existing()
        (self.bundle / 'music-completion.bash').write_text('corrupt')
        self.run_install(success=False)
        self.assertEqual((self.root / 'data/bin/music-service').read_text(), 'old executable')
        self.assertEqual((install / 'config.json').read_bytes(), config)
        self.assertTrue((self.root / 'active').exists())
        self.sums()
        sums = self.bundle / 'SHA256SUMS'
        sums.write_text('\n'.join(line for line in sums.read_text().splitlines() if not line.endswith('config.json.example')))
        self.run_install(success=False)
        self.assertEqual((self.root / 'data/bin/music-service').read_text(), 'old executable')

    def test_start_failure_rolls_back_existing_binary_config_and_hooks(self):
        install, config = self.existing()
        boot = (self.root / 'data/onboot.sh').read_bytes()
        (self.root / 'fail-start').touch()
        self.run_install(success=False)
        self.assertEqual((self.root / 'data/bin/music-service').read_text(), 'old executable')
        self.assertEqual((install / 'config.json').read_bytes(), config)
        self.assertEqual((self.root / 'data/onboot.sh').read_bytes(), boot)
        self.assertTrue((self.root / 'active').exists())
        self.assertFalse((self.root / 'data/.music-service-install.lock').exists())

    def test_unrelated_shortcut_and_unbalanced_boot_markers_are_rejected(self):
        self.existing()
        shortcut = self.root / 'usr/local/bin/music'
        shortcut.parent.mkdir(parents=True)
        shortcut.symlink_to('/unrelated/player')
        self.run_install(success=False)
        self.assertEqual(os.readlink(shortcut), '/unrelated/player')
        shortcut.unlink()
        (self.root / 'data/onboot.sh').write_text('#!/bin/sh\n# >>> music-service >>>\n')
        self.run_install(success=False)
        self.assertEqual((self.root / 'data/bin/music-service').read_text(), 'old executable')

    def test_boot_does_not_replace_unrelated_shortcut(self):
        self.existing()
        self.run_install()
        shortcut = self.root / 'usr/local/bin/music'
        shortcut.unlink()
        shortcut.symlink_to('/unrelated/player')
        result = subprocess.run(['sh', str(self.root / 'data/onboot.sh')], env=self.env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(os.readlink(shortcut), '/unrelated/player')

    def test_uninstall_keeps_songs_and_unrelated_boot_hook(self):
        install, config = self.existing()
        self.run_install()
        self.run_install('--uninstall')
        self.assertEqual((install / 'config.json').read_bytes(), config)
        self.assertEqual((install / 'songs/custom.bin').read_bytes(), b'private-user-data')
        self.assertNotIn('# >>> music-service >>>', (self.root / 'data/onboot.sh').read_text())
        self.assertIn('echo keep-this-hook', (self.root / 'data/onboot.sh').read_text())


if __name__ == '__main__':
    unittest.main()
