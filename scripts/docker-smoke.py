#!/usr/bin/env python3
"""Exercise a built panel image in an isolated Compose project (Python stdlib).

Creates and removes only its own temporary containers, network and data volume.
No Agent is installed and no host service is modified.
"""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import socket
import subprocess
import tempfile
import time
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlsplit
from urllib.request import Request, urlopen


ROOT = Path(__file__).resolve().parents[1]


def command(*args):
    return subprocess.check_output(args, cwd=ROOT, text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="singbox-panel:docker-test")
    args = parser.parse_args()
    project = "singbox-smoke-" + secrets.token_hex(5)
    password = secrets.token_urlsafe(24)
    account = "docker-smoke"
    token = ""
    base = ""

    def request(path, body=None, *, content_type="application/json", auth=None, status=200):
        headers = {"Content-Type": content_type}
        credential = token if auth is None else auth
        if credential:
            headers["Authorization"] = "Bearer " + credential
        if isinstance(body, dict):
            body = json.dumps(body).encode()
        req = Request(base + path, data=body, headers=headers)
        try:
            response = urlopen(req, timeout=45)
        except HTTPError as err:
            response = err
        with response:
            data = response.read()
            assert response.status == status, f"{path}: expected {status}, got {response.status}"
            return data, response.headers

    def api(path, body=None, **kwargs):
        return json.loads(request(path, body, **kwargs)[0])

    with tempfile.TemporaryDirectory(prefix=project) as temporary:
        env_file = Path(temporary) / "compose.env"
        env_file.write_text(
            f"PANEL_IMAGE={args.image}\nPANEL_BASE_URL=https://panel.example.test\n"
            f"PANEL_ADMIN={account}\nPANEL_ADMIN_PASSWORD={password}\n"
            "PANEL_BIND=127.0.0.1\nPANEL_PORT=0\n"
        )
        env_file.chmod(0o600)
        # An operator's shell/.env must not redirect this test to their panel.
        compose_env = {key: value for key, value in os.environ.items()
                       if not key.startswith("PANEL_") and not key.startswith("COMPOSE_")}

        def compose(*options):
            return subprocess.check_output(
                ["docker", "compose", "--project-name", project, "--env-file", str(env_file),
                 "--file", str(ROOT / "compose.yaml"), *options],
                cwd=ROOT, env=compose_env, text=True,
            ).strip()

        def start():
            nonlocal base
            compose("up", "-d", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "120")
            base = "http://" + compose("port", "panel", "8080")
            return compose("ps", "-q", "panel")

        def check_agent_download(agent_token):
            for arch, machine in (("amd64", 62), ("arm64", 183)):
                binary, headers = request("/api/agent/download?arch=" + arch, auth=agent_token)
                assert binary[:4] == b"\x7fELF" and int.from_bytes(binary[18:20], "little") == machine, arch
                assert hashlib.sha256(binary).hexdigest() == headers["X-SingBox-Panel-SHA256"], arch

        try:
            container = start()
            state = json.loads(command("docker", "inspect", container))[0]
            assert state["Config"]["User"] == "10001:10001"
            assert state["HostConfig"]["ReadonlyRootfs"] is True
            assert state["HostConfig"]["RestartPolicy"]["Name"] == "unless-stopped"
            assert api("/api/ready")["ok"] is True
            html = request("/")[0].decode()
            asset = re.search(r'(?:src|href)="(/assets/[^\"]+)"', html)
            assert asset, "frontend assets missing"
            request(asset[1])
            token = api("/api/auth/login", {"username": account, "password": password}, auth="")["token"]
            info = api("/api/admin/maintenance/info")
            assert info["deployment"] == "docker" and not info["update_supported"] and not info["reinstall_supported"]
            api("/api/admin/maintenance/update", {"force": True}, status=400)
            print("PASS: frontend, login, health, non-root/read-only runtime and maintenance capabilities", flush=True)

            server = api("/api/admin/servers", {"name": "Docker smoke node", "address": "198.51.100.10"})
            server_id = server["server"]["id"]
            words = shlex.split(server["install_command"])
            code = words[words.index("--code") + 1]
            agent_token = request("/api/agent/register", urlencode({"code": code}).encode(),
                                  content_type="application/x-www-form-urlencoded", auth="")[0].decode().strip()
            check_agent_download(agent_token)
            script = request("/api/agent/install.sh", auth="")[0].decode()
            assert "systemctl" in script, "native Agent installer missing"
            # Check the actual HTTP Upgrade path with a real Agent credential.
            endpoint = urlsplit(base)
            host, port = endpoint.hostname, endpoint.port
            key = base64.b64encode(secrets.token_bytes(16)).decode()
            with socket.create_connection((host, int(port)), timeout=10) as ws:
                ws.sendall((f"GET /api/agent/ws HTTP/1.1\r\nHost: {host}:{port}\r\n"
                            "Connection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\n"
                            f"Sec-WebSocket-Key: {key}\r\nAuthorization: Bearer {agent_token}\r\n\r\n").encode())
                handshake = b""
                while b"\r\n\r\n" not in handshake:
                    chunk = ws.recv(4096)
                    assert chunk, "WebSocket closed before handshake"
                    handshake += chunk
                assert handshake.startswith(b"HTTP/1.1 101 "), "Agent WebSocket upgrade failed"
            print("PASS: native Agent installer, both binary architectures/checksums and WebSocket upgrade", flush=True)

            backup = request("/api/admin/maintenance/backup")[0]
            api(f"/api/admin/servers/{server_id}")
            # Recreate, keeping the named volume but removing the bootstrap secret.
            env_file.write_text(env_file.read_text().replace(f"PANEL_ADMIN_PASSWORD={password}", "PANEL_ADMIN_PASSWORD="))
            compose("down")
            container = start()
            api("/api/user/me")  # Existing JWT still works: signing key persisted.
            assert api(f"/api/admin/servers/{server_id}")["server"]["name"] == "Docker smoke node"
            check_agent_download(agent_token)
            print("PASS: container recreation retains accounts, session key and Agent credentials", flush=True)

            # Add state not present in the backup, then restore it through the UI's API.
            added = api("/api/admin/servers", {"name": "After backup", "address": "198.51.100.11"})
            boundary = "singbox" + secrets.token_hex(12)
            multipart = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"backup.tar.gz\"\r\n"
                         "Content-Type: application/gzip\r\n\r\n").encode() + backup + f"\r\n--{boundary}--\r\n".encode()
            before = command("docker", "inspect", "--format", "{{.RestartCount}}", container)
            request("/api/admin/maintenance/restore", multipart, content_type="multipart/form-data; boundary=" + boundary)
            deadline = time.monotonic() + 90
            while time.monotonic() < deadline:
                after = command("docker", "inspect", "--format", "{{.RestartCount}}", container)
                if after != before:
                    # Ephemeral published ports can change after a Docker restart.
                    base = "http://" + compose("port", "panel", "8080")
                    try:
                        if api("/api/ready")["ok"]:
                            break
                    except (URLError, OSError, AssertionError):
                        pass
                time.sleep(1)
            else:
                raise AssertionError("restore did not restart the container and become ready")
            # Restore intentionally revokes sessions; authenticate against the restored DB.
            token = api("/api/auth/login", {"username": account, "password": password}, auth="")["token"]
            api(f"/api/admin/servers/{server_id}")
            api(f"/api/admin/servers/{added['server']['id']}", status=404)
            check_agent_download(agent_token)
            print("PASS: backup restore, automatic restart and restored database/Agent credentials", flush=True)
        except Exception:
            subprocess.run(["docker", "compose", "--project-name", project, "--env-file", str(env_file),
                            "--file", str(ROOT / "compose.yaml"), "logs", "--tail", "60"],
                           cwd=ROOT, env=compose_env, check=False)
            raise
        finally:
            # This unique project's volume contains only disposable smoke-test data.
            compose("down", "--volumes", "--remove-orphans")


if __name__ == "__main__":
    main()
