"""Exercise the local gate's scratch setup and failure cleanup without PostgreSQL."""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


RUNNER = Path(__file__).resolve().parents[1] / "qa-postgres.sh"
OVERRIDE = "QA_POSTGRES_SCRATCH_PARENT"


class ScratchTests(unittest.TestCase):
    def setUp(self):
        # Exercise another filesystem even if /tmp is full. The gate is not
        # meant to run as root and the test uses the same ownership rules.
        self.fixture = Path(self.enterContext(
            tempfile.TemporaryDirectory(prefix=".gomar-scratch-test.", dir=Path.home().resolve())
        ))
        # Never let a caller's Git paths or hooks redirect fixture operations.
        self.env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        self.env.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL="/dev/null")
        self.repo = self.fixture / "repo"
        self.repo.mkdir()
        (self.repo / "scripts").mkdir()
        shutil.copyfile(RUNNER, self.repo / "scripts" / RUNNER.name)
        (self.repo / ".gitignore").write_text("/frontend/node_modules/\n/.scratch/\n/.tools/\n")
        modules = self.repo / "frontend" / "node_modules"
        modules.mkdir(parents=True)
        (modules / "snapshot").write_text("dependency")
        tracked = self.repo / "frontend" / "tracked"
        tracked.write_text("source")
        for args in (("init", "-q"), ("add", "."), ("commit", "-qm", "fixture")):
            subprocess.run(
                ["git", "-C", self.repo, "-c", "user.name=Fixture", "-c",
                 "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", *args],
                check=True, capture_output=True, env=self.env,
            )
        self.parent = self.fixture / "private scratch"
        self.parent.mkdir(mode=0o700)
        (self.parent / "gomar-postgres-qa.existing").mkdir()
        self.trace = self.fixture / "trace"
        self.bin = self.fixture / "bin"
        self.bin.mkdir()
        for tool in ("initdb", "pg_ctl", "createdb", "dropdb", "go"):
            self.stub(tool, "exit 99")
        # Refuse the first build namespace after checking the staged inputs.
        self.stub("bwrap", r"""
set -eu
[[ "$1" == --die-with-parent && "$2" == --unshare-net ]]
[[ "$GOTMPDIR" == "$TMPDIR" && -d "$TMPDIR" ]]
scratch="${TMPDIR%/build-tmp}"
[[ $(cat "$scratch/source/frontend/tracked") == source ]]
[[ $(cat "$scratch/source/frontend/node_modules/snapshot") == dependency ]]
printf '%s\n' "$scratch" > "$TRACE"
case "${FAILURE:-}" in
  move-parent)
    parent="${scratch%/*}"
    mv -- "$parent" "$parent.moved"
    mkdir -m 700 -p -- "$scratch"
    touch -- "$scratch/sentinel"
    ;;
  replace-child)
    mv -- "$scratch" "$scratch.moved"
    mkdir -m 700 -- "$scratch"
    touch -- "$scratch/sentinel"
    ;;
  terminate)
    kill -TERM "$PPID"
    ;;
esac
exit 42""")

    def stub(self, name, source):
        path = self.bin / name
        path.write_text(f"#!/usr/bin/env bash\n{source}\n")
        path.chmod(0o700)

    def run_gate(self, parent=None, **env_overrides):
        env = dict(self.env, PATH=f"{self.bin}:{self.env['PATH']}",
                   QA_PG_BIN=str(self.bin), QA_PYTHON=sys.executable, TRACE=str(self.trace))
        env.pop(OVERRIDE, None)
        if parent is not None:
            env[OVERRIDE] = str(parent)
        return subprocess.run(["bash", self.repo / "scripts" / RUNNER.name],
                              cwd=self.repo, env={**env, **env_overrides},
                              capture_output=True, text=True)

    def test_rejects_unsafe_overrides_before_build(self):
        (link := self.fixture / "link").symlink_to(self.parent)
        (shared := self.fixture / "shared").mkdir()
        shared.chmod(0o777)
        (through_shared := shared / "private").mkdir(mode=0o700)
        (inside := self.repo / ".scratch").mkdir(mode=0o700)
        for parent in ("", "relative", "/", "/tmp", self.fixture / "missing",
                       str(self.parent) + "/.", link, shared, through_shared, inside):
            with self.subTest(parent=parent):
                result = self.run_gate(parent)
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertIn(OVERRIDE, result.stderr)
                self.assertFalse(self.trace.exists())
                self.assertEqual(result.stdout, "")

    def test_default_still_allocates_under_tmp(self):
        # Check selection without writing to the shared filesystem.
        self.stub("mktemp", 'printf "%s\\n" "$PWD" "$@" > "$TRACE"; exit 42')
        result = self.run_gate(TMPDIR=str(self.parent))
        self.assertEqual(result.returncode, 42, result.stderr)
        self.assertEqual(self.trace.read_text().splitlines(),
                         ["/tmp", "-d", "--", "gomar-postgres-qa.XXXXXXXX"])
        self.assertEqual(result.stdout, "")

    def test_build_failure_cleans_only_the_allocated_child(self):
        result = self.run_gate(self.parent)
        self.assertEqual(result.returncode, 42, result.stderr)
        scratch = Path(self.trace.read_text().strip())
        self.assertEqual(scratch.parent, self.parent)
        self.assertEqual(list(self.parent.iterdir()), [self.parent / "gomar-postgres-qa.existing"])
        self.assertEqual(result.stdout, "")

    def test_relative_path_tool_uses_the_original_worktree(self):
        tools = self.repo / ".tools"
        tools.mkdir()
        (self.bin / "bwrap").rename(tools / "bwrap")
        (decoy := self.parent / ".tools").mkdir()
        (decoy / "cp").write_text("#!/usr/bin/env bash\nexit 99\n")
        (decoy / "cp").chmod(0o700)
        result = self.run_gate(self.parent, PATH=f"./.tools:{self.bin}:{self.env['PATH']}")
        self.assertEqual(result.returncode, 42, result.stderr)
        self.assertEqual(Path(self.trace.read_text().strip()).parent, self.parent)
        self.assertEqual(result.stdout, "")

    def test_cleanup_does_not_follow_a_replaced_parent_path(self):
        result = self.run_gate(self.parent, FAILURE="move-parent")
        self.assertEqual(result.returncode, 42, result.stderr)
        replacement = Path(self.trace.read_text().strip())
        self.assertTrue((replacement / "sentinel").is_file())
        moved_parent = self.parent.with_name(self.parent.name + ".moved")
        self.assertEqual(list(moved_parent.iterdir()), [moved_parent / "gomar-postgres-qa.existing"])
        self.assertEqual(result.stdout, "")

    def test_cleanup_refuses_a_replacement_child(self):
        result = self.run_gate(self.parent, FAILURE="replace-child")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("scratch directory changed", result.stderr)
        scratch = Path(self.trace.read_text().strip())
        self.assertTrue((scratch / "sentinel").is_file())
        self.assertTrue(scratch.with_name(scratch.name + ".moved").is_dir())
        self.assertEqual(result.stdout, "")

    def test_termination_cleans_the_allocated_child(self):
        result = self.run_gate(self.parent, FAILURE="terminate")
        self.assertEqual(result.returncode, 143, result.stderr)
        scratch = Path(self.trace.read_text().strip())
        self.assertFalse(scratch.exists())
        self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
