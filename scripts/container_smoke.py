#!/usr/bin/env python3
"""Exercise an exact locally built image using isolated, disposable fixtures."""
import base64
from contextlib import ExitStack
import hashlib
import hmac
import http.client
import json
import os
from pathlib import Path
import re
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import uuid


def run(*args, **kwargs):
    return subprocess.check_output(args, text=True, timeout=240, **kwargs).strip()


def pdf_bytes():
    stream = b"BT /F1 12 Tf 30 50 Td (Iulita image smoke) Tj ET"
    objects = [b"<< /Type /Catalog /Pages 2 0 R >>", b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
               b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 100] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
               b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
               b"<< /Length " + str(len(stream)).encode() + b" >>\nstream\n" + stream + b"\nendstream"]
    data, offsets = b"%PDF-1.4\n", [0]
    for number, obj in enumerate(objects, 1):
        offsets.append(len(data))
        data += str(number).encode() + b" 0 obj\n" + obj + b"\nendobj\n"
    start = len(data)
    data += b"xref\n0 6\n0000000000 65535 f \n"
    data += b"".join(f"{offset:010d} 00000 n \n".encode() for offset in offsets[1:])
    return data + f"trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n{start}\n%%EOF\n".encode()


def request(port, context, path, body=None, signature=None, host=None):
    conn = http.client.HTTPSConnection("127.0.0.1", port, context=context, timeout=10)
    try:
        headers = {"Content-Type": "application/json"}
        if host:
            headers["Host"] = host
        if signature:
            headers["X-Iulita-Signature"] = signature
        conn.request("POST" if body is not None else "GET", path, body=body, headers=headers)
        response = conn.getresponse()
        return response.status, response.read(1 << 20)
    finally:
        conn.close()


def wait_for(check):
    deadline = time.monotonic() + 90
    while True:
        try:
            return check()
        except (OSError, subprocess.SubprocessError, http.client.HTTPException):
            if time.monotonic() >= deadline:
                raise
            time.sleep(1)


class Fixture:
    def __init__(self, image, platform):
        self.image, self.platform = image, platform
        self.names = []
        self.networks = []

    def oneshot(self, entrypoint, *args, mounts=(), env=(), network="none"):
        name = "iulita-imagecheck-" + uuid.uuid4().hex
        self.names.append(name)
        command = ["docker", "run", "--rm", "--name", name, "--platform", self.platform, "--network", network,
                   "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
                   "--memory=2g", "--cpus=2", "--tmpfs", "/tmp:rw,nosuid,size=512m"]
        for mount in mounts:
            command += ["-v", mount]
        for value in env:
            command += ["-e", value]
        return run(*command, "--entrypoint", entrypoint, self.image, *args)

    def start(self, *args, mounts=(), env=(), entrypoint=None, port=None, offline=False, network=None, alias=None):
        name = "iulita-imagecheck-" + uuid.uuid4().hex
        command = ["docker", "run", "-d", "--name", name, "--platform", self.platform,
                   "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
                   "--memory=512m", "--cpus=2", "--tmpfs", "/tmp:rw,nosuid,size=128m"]
        if offline:
            command += ["--network", "none"]
        elif network:
            command += ["--network", network]
        if alias:
            command += ["--network-alias", alias]
        if port:
            command += ["-p", f"127.0.0.1::{port}"]
        for mount in mounts:
            command += ["-v", mount]
        for value in env:
            command += ["-e", value]
        if entrypoint:
            command += ["--entrypoint", entrypoint]
        self.names.append(name)
        run(*command, self.image, *args)
        return name

    def close(self):
        for name in self.names:
            subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)
        for network in self.networks:
            subprocess.run(["docker", "network", "rm", network], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)

    def require_running(self, name):
        state = json.loads(run("docker", "inspect", "--format", "{{json .State}}", name))
        if not state["Running"]:
            logs = run("docker", "logs", name, stderr=subprocess.STDOUT)
            known = ["SSH worker must run as a non-root user", "worker key file must be private (0o600)",
                     "cannot read worker key", "SSH worker state directory must be private (0o700)",
                     "worker TLS certificate/key unavailable or invalid", "worker TLS certificate outside validity period",
                     "permission denied", "operation not permitted"]
            reason = next((item for item in known if item in logs), "unrecognized startup failure")
            raise RuntimeError(f"Fixture exited with code {state['ExitCode']}: {reason}")


def app(fixture, directory, tools):
    assert fixture.oneshot("id", "-u") == "1000"
    assert "v" in fixture.oneshot("iulita", "--version")
    helper = (tools / "imagecheck").resolve()
    assert helper.is_file()
    fixture.oneshot("/imagecheck", mounts=[f"{helper}:/imagecheck:ro"])
    pdf = directory / "input.pdf"
    pdf.write_bytes(pdf_bytes())
    pdf.chmod(0o644)
    assert "Iulita image smoke" in fixture.oneshot("pdftotext", "/input.pdf", "-", mounts=[f"{pdf}:/input.pdf:ro"])
    fixture.oneshot("sh", "-ec", "ffmpeg -loglevel error -f lavfi -i sine=frequency=440 -t 0.2 -c:a libopus /tmp/input.ogg; "
                    "ffmpeg -loglevel error -i /tmp/input.ogg -c:a libmp3lame /tmp/output.mp3; test -s /tmp/output.mp3")
    name = fixture.start(offline=True, env=["IULITA_HOME=/tmp/imagecheck-home"])
    def system():
        return json.loads(run("docker", "exec", name, "wget", "-q", "-O", "-", "http://127.0.0.1:8080/api/system"))
    info = wait_for(system)
    assert info["app"] == "iulita" and info["setup_mode"] is True
    assert "go1.27.4" == info["go_version"], info["go_version"]
    return {"sqlite_roundtrip": True, "offline_embedding": True, "pdf_text": True,
            "ogg_opus_to_mp3": True, "setup_api": True, "go_version": info["go_version"]}


def certificates(directory):
    cert, key = directory / "server.crt", directory / "server.key"
    subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                    "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost,DNS:backend,DNS:gateway,DNS:mobile.fixture,IP:127.0.0.1",
                    "-keyout", str(key), "-out", str(cert)], check=True, capture_output=True)
    cert.chmod(0o644)
    return cert, key


def published_port(name, port):
    return int(run("docker", "port", name, f"{port}/tcp").rsplit(":", 1)[1])


def worker(fixture, directory):
    assert fixture.oneshot("id", "-u") == "10001"
    directory.chmod(0o755)
    cert, tls_key = certificates(directory)
    key = os.urandom(32)
    protocol_key = directory / "protocol.key"
    protocol_key.write_bytes(key)
    protocol_key.chmod(0o600)
    state = directory / "state"
    state.mkdir(mode=0o700)
    run("sudo", "chown", "10001:10001", str(protocol_key), str(tls_key), str(state))
    with ExitStack() as cleanup:
        # Independent LIFO cleanup also runs if later fixture setup fails.
        cleanup.callback(run, "sudo", "chown", "-R", f"{os.getuid()}:{os.getgid()}", str(state))
        # A disposable account with no sudo authority, and an enforced fixed command.
        # Even a PR-controlled worker cannot turn this key into host runner access.
        hostkey, clientkey = directory / "ssh-host", directory / "ssh-client"
        for path in [hostkey, clientkey]:
            run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(path))
        user = "iulita-ci-" + uuid.uuid4().hex[:8]
        assert re.fullmatch(r"[a-z_][a-z0-9_-]*", user)
        run("sudo", "useradd", "--system", "--no-create-home", "--home-dir", "/nonexistent",
            "--shell", "/bin/sh", "--password", "*", user)
        cleanup.callback(run, "sudo", "userdel", user)
        cleanup.callback(subprocess.run, ["sudo", "pkill", "-u", user], capture_output=True, timeout=15)
        gateway = json.loads(run("docker", "network", "inspect", "bridge"))[0]["IPAM"]["Config"][0]["Gateway"]
        with socket.socket() as sock:
            sock.bind((gateway, 0))
            ssh_port = sock.getsockname()[1]
        config, pidfile = directory / "sshd.conf", directory / "sshd.pid"
        config.write_text(f"Port {ssh_port}\nListenAddress {gateway}\nHostKey {hostkey}\nPidFile {pidfile}\n"
                          f"AuthorizedKeysFile {clientkey}.pub\nAllowUsers {user}\nStrictModes no\n"
                          "PasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nLogLevel ERROR\n"
                          "ForceCommand /usr/bin/printf iulita-container-smoke\nDisableForwarding yes\n"
                          "PermitTTY no\nPermitUserEnvironment no\nPermitUserRC no\nPermitTunnel no\n"
                          "ClientAliveInterval 1\nClientAliveCountMax 1\nMaxSessions 1\nMaxStartups 3\n")
        run("sudo", "mkdir", "-p", "/run/sshd")
        daemon = subprocess.Popen(["sudo", "/usr/sbin/sshd", "-D", "-e", "-f", str(config)],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        def stop_daemon():
            if pidfile.exists():
                pid = pidfile.read_text().strip()
                if pid.isdigit():
                    subprocess.run(["sudo", "kill", "--", pid], capture_output=True, timeout=15)
            if daemon.poll() is None:
                subprocess.run(["sudo", "kill", "--", str(daemon.pid)], capture_output=True, timeout=15)
            daemon.wait(timeout=15)
        cleanup.callback(stop_daemon)
        cleanup.callback(fixture.close)
        def ssh_ready():
            with socket.create_connection((gateway, ssh_port), timeout=2):
                return True
        wait_for(ssh_ready)
        name = fixture.start("--tls-cert", "/fixture/server.crt", "--tls-key", "/fixture/server.key",
                             "--key-file", "/fixture/protocol.key", "--state-dir", "/state",
                             "--allow-cidrs", f"{gateway}/32", mounts=[f"{directory}:/fixture:ro", f"{state}:/state:rw"], port=8443)
        port = published_port(name, 8443)
        context = ssl.create_default_context(cafile=str(cert))
        def worker_ready():
            fixture.require_running(name)
            return request(port, context, "/")
        assert wait_for(worker_ready)[0] == 404
        assert request(port, context, "/execute", b"{}", "invalid")[0] == 401
        def envelope(address):
            return json.dumps({"id": uuid.uuid4().hex, "issued_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                               "request": {"Endpoints": [{"Address": address, "Port": ssh_port, "Username": user,
                                                         "HostKeys": [hostkey.with_suffix(".pub").read_text()],
                                                         "PrivateKey": clientkey.read_text()}],
                                           "Command": "printf should-not-execute", "Timeout": 10_000_000_000}}, separators=(",", ":")).encode()
        def signed(body):
            return request(port, context, "/execute", body, hmac.new(key, body, hashlib.sha256).hexdigest())
        assert signed(envelope("192.0.2.1"))[0] == 403
        body = envelope(gateway)
        status, raw = signed(body)
        reply = json.loads(raw)
        assert status == 200 and reply["failed"] is False, "isolated SSH execution failed"
        assert base64.b64decode(reply["result"]["Stdout"]) == b"iulita-container-smoke"
        assert signed(body)[0] == 409
        run("docker", "restart", name)
        assert wait_for(lambda: request(port, context, "/"))[0] == 404
        replay = json.loads(body)
        replay["issued_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        assert signed(json.dumps(replay, separators=(",", ":")).encode())[0] == 409
        return {"tls_verified": True, "bad_hmac_denied": True, "allow_cidr_denied": True,
                "fixture_ssh_exec": True, "fixture_forced_command": True, "replay_denied_across_restart": True}


def gateway(fixture, directory, tools):
    assert fixture.oneshot("id", "-u") == "101"
    directory.chmod(0o755)
    cert, key = certificates(directory)
    key.chmod(0o644)  # Disposable fixture certificate, never a deployment key.
    helper = (tools / "gatewaycheck").resolve()
    assert helper.is_file()
    network = "iulita-smoke-" + uuid.uuid4().hex
    run("docker", "network", "create", "--internal", network)
    fixture.networks.append(network)
    fixture.start(entrypoint="/gatewaycheck", network=network, alias="backend",
                  mounts=[f"{directory}:/fixture:ro", f"{helper}:/gatewaycheck:ro"])
    config = directory / "nginx.conf"
    config.write_text("pid /tmp/nginx.pid; error_log /dev/stderr; events {}\nhttp {\n"
                      "client_body_temp_path /tmp/client; proxy_temp_path /tmp/proxy; fastcgi_temp_path /tmp/fastcgi; "
                      "uwsgi_temp_path /tmp/uwsgi; scgi_temp_path /tmp/scgi;\n"
                      "server { listen 8445 ssl; http2 on; ssl_certificate /fixture/server.crt; "
                      "ssl_certificate_key /fixture/server.key;\n"
                      "location = /ready { return 200 'ready'; }\n"
                      "location = /bad-upstream-name { proxy_pass https://backend:8444/oauth2/jwks; proxy_set_header Host $host; "
                      "proxy_ssl_server_name on; proxy_ssl_name invalid.fixture; proxy_ssl_verify on; "
                      "proxy_ssl_trusted_certificate /fixture/server.crt; proxy_ssl_protocols TLSv1.3; }\n"
                      "location ^~ /iulita.mobile.v1. { grpc_pass grpcs://backend:8444; grpc_set_header Host $host; "
                      "grpc_set_header Authorization $http_authorization; grpc_ssl_server_name on; grpc_ssl_name backend; "
                      "grpc_ssl_verify on; grpc_ssl_trusted_certificate /fixture/server.crt; grpc_ssl_protocols TLSv1.3; }\n"
                      "location / { proxy_pass https://backend:8444; proxy_set_header Host $host; proxy_http_version 1.1; "
                      "proxy_ssl_server_name on; proxy_ssl_name backend; proxy_ssl_verify on; "
                      "proxy_ssl_trusted_certificate /fixture/server.crt; proxy_ssl_protocols TLSv1.3; } } }\n")
    config.chmod(0o644)
    fixture.start("-c", "/fixture/nginx.conf", "-g", "daemon off;", entrypoint="nginx",
                  mounts=[f"{directory}:/fixture:ro"], network=network, alias="gateway")
    def curl(path, host="mobile.fixture", grpc=False, authorized=False):
        args = ["--silent", "--show-error", "--max-time", "10", "--http2", "--noproxy", "*",
                "--cacert", "/server.crt", "-H", "Host: " + host, "--write-out", "\n%{http_code}\n%{http_version}"]
        mounts = [f"{cert}:/server.crt:ro"]
        if grpc:
            message = directory / "request.grpc"
            message.write_bytes(b"\0\0\0\0\0")
            message.chmod(0o644)
            mounts += [f"{message}:/request.grpc:ro"]
            args += ["-H", "Content-Type: application/grpc", "-H", "TE: trailers", "--data-binary", "@/request.grpc", "--dump-header", "-"]
            if authorized:
                args += ["-H", "Authorization: Bearer isolated-fixture"]
        raw = fixture.oneshot("curl", *args, "https://gateway:8445" + path, mounts=mounts, network=network)
        body, status, protocol = raw.rsplit("\n", 2)
        return body, int(status), protocol
    def metadata():
        body, status, protocol = curl("/.well-known/oauth-authorization-server")
        if status != 200:
            raise OSError("fixture backend not ready")
        assert protocol.startswith("2")
        return json.loads(body)
    assert wait_for(metadata)["issuer"] == "https://mobile.fixture"
    assert curl("/.well-known/oauth-authorization-server", host="wrong.fixture")[1] == 400
    assert curl("/bad-upstream-name")[1] == 502
    assert json.loads(curl("/oauth2/jwks")[0])["keys"][0]["kty"] == "RSA"
    assert curl("/protected")[1] == 401
    for authorized in [False, True]:
        body, status, protocol = curl("/iulita.mobile.v1.Smoke/Check", grpc=True, authorized=authorized)
        assert status == 200 and protocol.startswith("2")
        assert "x-iulita-smoke-backend: verified" in body.lower()
        assert f"grpc-status: {0 if authorized else 16}" in body.lower()
        if authorized:
            assert "\0\0\0\0\2\10\1" in body
    # Version inspection runs in a separate one-shot container of the same image.
    version = fixture.oneshot("sh", "-c", "nginx -v 2>&1")
    found = re.search(r"nginx/(\d+)\.(\d+)\.(\d+)", version)
    assert found and tuple(map(int, found.groups())) >= (1, 30, 6), version
    return {"tls_verified": True, "http2_alpn": True, "fixture_tls13_proxy_host_preserved": True,
            "fixture_wrong_host_denied": True, "fixture_wrong_upstream_tls_name_denied": True,
            "fixture_metadata_jwks_proxy": True, "fixture_grpc_frame_and_trailer_proxy": True,
            "fixture_grpc_auth_denial": True, "nginx": version}


def main():
    role, image, platform, output, tools = sys.argv[1:]
    fixture = Fixture(image, platform)
    try:
        with tempfile.TemporaryDirectory(prefix="iulita-container-smoke-") as temp:
            directory = Path(temp)
            result = {"app": lambda: app(fixture, directory, Path(tools)),
                      "ssh-worker": lambda: worker(fixture, directory),
                      "mobile-gateway": lambda: gateway(fixture, directory, Path(tools))}[role]()
            result["os_release"] = fixture.oneshot("cat", "/etc/os-release")
            assert "VERSION_ID=3.24." in result["os_release"]
            result["role"], result["platform"] = role, platform
            Path(output).write_text(json.dumps(result, indent=2) + "\n")
            print(json.dumps(result))
    except Exception as error:
        # Keep diagnostics even if a fixture fails before producing its receipt.
        # Only an exception class and known startup classification are retained.
        failure = {"role": role, "platform": platform, "passed": False, "error_type": type(error).__name__}
        if isinstance(error, RuntimeError) and str(error).startswith("Fixture exited with code "):
            failure["startup_error"] = str(error)
        Path(output).write_text(json.dumps(failure, indent=2) + "\n")
        raise
    finally:
        fixture.close()


if __name__ == "__main__":
    main()
