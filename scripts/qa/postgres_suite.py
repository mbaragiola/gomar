"""Run observed PostgreSQL tests in isolated, fresh databases."""
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import pwd
import re
import subprocess
import sys
import time

NEEDS_DB = "requires a fresh disposable local PostgreSQL database"
WORKERS = 4
PORT = "55432"
root_arg, pg_arg, json_arg, commit, tree = sys.argv[1:]
root, pg, test2json = map(Path, (root_arg, pg_arg, json_arg))
user = pwd.getpwuid(os.getuid()).pw_name


def make_env():
    env = {key: os.environ[key] for key in ("PATH", "LD_LIBRARY_PATH", "LANG", "LC_ALL", "TZ")
           if key in os.environ}
    for name in ("home", "tmp", "unix", "socket"):
        (root / name).mkdir(mode=0o700)
    # Do not inherit other environment settings or credential sources.
    env.update(HOME=str(root / "home"), TMPDIR=str(root / "tmp"),
               GOMAR_UNIX_TEST_PARENT=str(root / "unix"))
    return env


def invoke(command, env):
    return subprocess.run(command, check=True, env=env, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.STDOUT)


def test_events(env, *flags):
    command = [test2json, root / "app-tests", "-test.v=test2json", *flags]
    result = subprocess.run(command, env=env, text=True, capture_output=True, check=False)
    return result, [json.loads(line) for line in result.stdout.splitlines()]


def discover(env):
    result, events = test_events(env)
    if result.returncode or not any(e["Action"] == "pass" and "Test" not in e for e in events):
        failed = ", ".join(sorted(e["Test"] for e in events if e["Action"] == "fail" and e.get("Test")))
        raise RuntimeError("Gomar no-database discovery failed" + (": " + failed if failed else ""))
    reasons = {e["Test"] for e in events if e.get("Test") and e["Action"] == "output"
               and NEEDS_DB in e.get("Output", "")}
    skipped = {e["Test"] for e in events if e["Action"] == "skip" and e.get("Test")}
    if not reasons or not reasons.issubset(skipped):
        raise RuntimeError("Gomar no-database discovery did not produce consistent scenario names")
    return sorted(reasons)


def setup(env):
    invoke([pg / "initdb", "-D", root / "data", "--locale=C", "--encoding=UTF8",
            "--auth=trust"], env)
    invoke([pg / "pg_ctl", "-D", root / "data", "-l", root / "postgres.log", "-w", "start",
            "-o", f"-k {root}/socket -p {PORT} -c listen_addresses=''"], env)


def db_command(command, db):
    return [pg / command, "-h", root / "socket", "-p", PORT, "-U", user, db]


def exact_regex(name):
    # Go's -test.run matches slash-separated parts independently.
    escape = re.compile(r"([\\.^$|?*+()\[\]{}])")
    return "/".join("^" + escape.sub(r"\\\1", part) + "$" for part in name.split("/"))


def check(item, env):
    index, name = item
    started = time.monotonic()
    db = f"gomar_qa_isolated_{index}"
    try:
        invoke(db_command("createdb", db), env)
        try:
            dsn = f"host={root}/socket port={PORT} user={user} dbname={db} sslmode=disable"
            result, events = test_events({**env, "GOMAR_LOCAL_POSTGRES_TEST_DSN": dsn},
                                         "-test.run", exact_regex(name))
            if (result.returncode or not any(e["Action"] == "pass" and e.get("Test") == name for e in events)
                    or any(e["Action"] == "skip" for e in events)):
                output = "".join(e.get("Output", "") for e in events)
                return {"name": name, "passed": False}, output
        finally:
            invoke(db_command("dropdb", db), env)
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        return {"name": name, "passed": False}, f"{type(exc).__name__} in disposable fixture\n"
    return {"name": name, "passed": True, "elapsed_seconds": round(time.monotonic() - started, 3)}, ""


def main():
    started = time.monotonic()
    env = make_env()
    names = discover(env)
    discovered = time.monotonic()
    setup(env)
    setup_finished = time.monotonic()
    try:
        with concurrent.futures.ThreadPoolExecutor(max_workers=WORKERS) as workers:
            results = list(workers.map(lambda item: check(item, env), enumerate(names, 1)))
        execution_finished = time.monotonic()
    finally:
        try:
            invoke([pg / "pg_ctl", "-D", root / "data", "-m", "fast", "-w", "stop"], env)
        except subprocess.CalledProcessError:
            raise RuntimeError("Disposable PostgreSQL shutdown failed; no pass manifest") from None
        status = subprocess.run([pg / "pg_ctl", "-D", root / "data", "status"], env=env,
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        if status.returncode != 3 or (root / "data" / "postmaster.pid").exists():
            raise RuntimeError("Could not verify disposable PostgreSQL shutdown; no pass manifest")
    failures = [(case, detail) for case, detail in results if not case["passed"]]
    if failures:
        for case, detail in failures:
            print(f"{case['name']} did not explicitly pass:\n{detail}", file=sys.stderr)
        raise SystemExit(1)
    with (root / "app-tests").open("rb") as binary:
        sha256 = hashlib.file_digest(binary, "sha256").hexdigest()
    manifest = {
        "schema": 1,
        "kind": "isolated_postgres_only",
        "commit": commit,
        "source_tree": tree,
        "test_binary_sha256": sha256,
        "workers": WORKERS,
        "discovery_seconds": round(discovered - started, 3),
        "setup_seconds": round(setup_finished - discovered, 3),
        "execution_seconds": round(execution_finished - setup_finished, 3),
        "shutdown_seconds": round(time.monotonic() - execution_finished, 3),
        "passed": len(results),
        "cases": [case for case, _ in results],
    }
    (root / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
