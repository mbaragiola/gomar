"""Use a local installed Codex to invoke tools, not to run a prompt/model."""
from contextlib import contextmanager
import json
import os
from pathlib import Path
import subprocess
import tempfile


@contextmanager
def codex_tools(executable, mcp_command, root):
    with tempfile.TemporaryDirectory(prefix="codex-session-", dir=root) as directory:
        private = Path(directory)
        settings = private / "settings"
        settings.mkdir(mode=0o700)
        # No login, user's agent config, provider keys, project config, or
        # external network. The parent runs both processes in isolated loopback.
        env = {
            "PATH": os.environ["PATH"],
            "HOME": directory,
            "CODEX_HOME": str(settings),
            "XDG_CONFIG_HOME": directory,
            "XDG_DATA_HOME": directory,
            "XDG_CACHE_HOME": directory,
            "TMPDIR": directory,
            "LANG": "C.UTF-8",
            "DISABLE_TELEMETRY": "1",
            "DISABLE_ERROR_REPORTING": "1",
            "RUST_LOG": "off",
        }
        if "LD_LIBRARY_PATH" in os.environ:
            env["LD_LIBRARY_PATH"] = os.environ["LD_LIBRARY_PATH"]
        version = subprocess.run([executable, "--version"], cwd=private, env=env, text=True,
                                 stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=True, timeout=10)
        print(f"QA client: {version.stdout.strip()}", flush=True)
        args = [executable, "app-server", "-c", "analytics.enabled=false",
                "-c", "mcp_servers.gomar.command=" + json.dumps(str(mcp_command[0])),
                "-c", "mcp_servers.gomar.args=" + json.dumps([str(a) for a in mcp_command[1:]])]
        with subprocess.Popen(args, cwd=private, env=env, text=True, stdin=subprocess.PIPE,
                              stdout=subprocess.PIPE, stderr=subprocess.DEVNULL) as process:
            next_id = 0

            def rpc(method, params=None, notify=False):
                nonlocal next_id
                next_id += 1
                payload = {"method": method, "params": params or {}}
                if not notify:
                    payload["id"] = next_id
                process.stdin.write(json.dumps(payload) + "\n")
                process.stdin.flush()
                if notify:
                    return None
                for line in process.stdout:
                    response = json.loads(line)
                    if response.get("id") == next_id:
                        if "result" not in response or "error" in response:
                            raise RuntimeError(f"Installed Codex rejected QA RPC {method}")
                        return response["result"]
                raise RuntimeError(f"Installed Codex stopped during QA RPC {method}")

            try:
                rpc("initialize", {"clientInfo": {"name": "gomar-stock-journey-qa", "version": "1"},
                                   "capabilities": {"experimentalApi": True}})
                rpc("initialized", notify=True)
                status = rpc("mcpServerStatus/list", {"serverName": "gomar", "detail": "toolsAndAuthOnly"})
                tools = status["data"][0]["tools"]
                thread = rpc("thread/start", {
                    "cwd": directory, "ephemeral": True, "approvalPolicy": "never", "sandbox": "read-only",
                })["thread"]["id"]

                def invoke(name, arguments):
                    return rpc("mcpServer/tool/call", {"threadId": thread, "server": "gomar",
                                                     "tool": name, "arguments": arguments})

                yield list(tools.values()), invoke
            finally:
                process.stdin.close()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
