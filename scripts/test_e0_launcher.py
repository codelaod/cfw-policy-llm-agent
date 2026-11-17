#!/usr/bin/env python3
"""Offline launcher contract tests; the Go command is replaced by an argv recorder."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent


class LauncherArgumentsTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="cfw-launcher-test-")
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name).resolve()
        self.capture = self.directory / "go-argv.json"
        stub = self.directory / "go"
        stub.write_text(
            "#!/usr/bin/env python3\n"
            "import json, os, pathlib, sys\n"
            "pathlib.Path(os.environ['CFW_LAUNCHER_TEST_CAPTURE']).write_text("
            "json.dumps({'argv': sys.argv[1:], 'cwd': os.getcwd(), 'paths': {key: os.environ.get(key) for key in ['CFW_DB_PATH', 'CFW_WEB_DIR']}}))\n",
            encoding="utf-8",
        )
        stub.chmod(0o700)
        self.environment = {
            "PATH": str(self.directory) + os.pathsep + os.defpath,
            "CFW_LAUNCHER_TEST_CAPTURE": str(self.capture),
        }

    def launch(self, *arguments, cwd=None):
        return subprocess.run(
            ["bash", str(ROOT / "scripts" / "e0.sh"), *arguments],
            cwd=cwd or self.directory,
            env=self.environment,
            capture_output=True,
            text=True,
            check=False,
        )

    def test_server_forwards_arguments_without_changing_boundaries(self):
        arguments = ["--env-file", "../private path/project.env", "--config-check"]
        result = self.launch("server", *arguments)
        self.assertEqual(result.returncode, 0, result.stderr)
        recorded = json.loads(self.capture.read_text(encoding="utf-8"))
        self.assertEqual(recorded["argv"], ["run", "-mod=readonly", "./cmd/server", "--env-file", str(self.directory / "../private path/project.env"), "--config-check"])
        self.assertEqual(recorded["cwd"], str(ROOT / "server"))

    def test_migrate_forwards_selected_database_path(self):
        arguments = ["--env-file", "../private path/project.env"]
        result = self.launch("migrate", *arguments)
        self.assertEqual(result.returncode, 0, result.stderr)
        recorded = json.loads(self.capture.read_text(encoding="utf-8"))
        self.assertEqual(recorded["argv"], ["run", "-mod=readonly", "./cmd/server", "--migrate", "--env-file", str(self.directory / "../private path/project.env")])


    def test_explicit_relative_paths_are_based_on_caller_directory(self):
        for flag in ["--env-file", "--budget-change", "-env-file", "-budget-change"]:
            for joined in [False, True]:
                with self.subTest(flag=flag, joined=joined):
                    value = "data/private path/selected file"
                    arguments = [flag + "=" + value] if joined else [flag, value]
                    result = self.launch("server", *arguments, cwd=ROOT)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    recorded = json.loads(self.capture.read_text(encoding="utf-8"))
                    absolute = str(ROOT / value)
                    expected = [flag + "=" + absolute] if joined else [flag, absolute]
                    self.assertEqual(recorded["argv"], ["run", "-mod=readonly", "./cmd/server", *expected])

    def test_absolute_paths_empty_values_and_non_path_arguments_remain_exact(self):
        absolute = str(self.directory / "private path/selected.json")
        arguments = ["--env-file", absolute, "--budget-change=" + absolute, "--env-file", "", "--env-file=", "--unknown-option=relative/text", "--", "--env-file", "literal/after-terminator"]
        result = self.launch("server", *arguments)
        self.assertEqual(result.returncode, 0, result.stderr)
        recorded = json.loads(self.capture.read_text(encoding="utf-8"))
        self.assertEqual(recorded["argv"], ["run", "-mod=readonly", "./cmd/server", *arguments])

    def test_missing_path_value_and_default_database_are_left_to_server(self):
        for mode, arguments, fixed in [("server", ["--env-file"], []), ("server", [], []), ("migrate", [], ["--migrate"])]:
            with self.subTest(mode=mode, arguments=arguments):
                result = self.launch(mode, *arguments)
                self.assertEqual(result.returncode, 0, result.stderr)
                recorded = json.loads(self.capture.read_text(encoding="utf-8"))
                self.assertEqual(recorded["argv"], ["run", "-mod=readonly", "./cmd/server", *fixed, *arguments])

    def test_process_paths_keep_the_callers_directory_and_explicit_empty(self):
        self.environment['CFW_DB_PATH'] = 'private folder/database.sqlite'
        self.environment['CFW_WEB_DIR'] = ''
        result = self.launch('server', '--config-check')
        self.assertEqual(result.returncode, 0, result.stderr)
        recorded = json.loads(self.capture.read_text(encoding='utf-8'))
        self.assertEqual(recorded['paths']['CFW_DB_PATH'], str(self.directory / 'private folder/database.sqlite'))
        self.assertEqual(recorded['paths']['CFW_WEB_DIR'], '')

    def test_other_modes_reject_extra_arguments_before_running_go(self):
        for mode in ["test", "build", "api", "help", "unknown-mode"]:
            with self.subTest(mode=mode):
                result = self.launch(mode, "--env-file", "unused.env")
                self.assertNotEqual(result.returncode, 0, "extra arguments were silently accepted")
                self.assertFalse(self.capture.exists(), "Go ran despite unsupported launcher arguments")
                self.assertIn("does not accept extra arguments", result.stderr)

    def test_default_usage_remains_local(self):
        result = self.launch()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Usage:", result.stdout)
        self.assertFalse(self.capture.exists())


if __name__ == "__main__":
    unittest.main()
