#!/usr/bin/env python3
"""Verify the bounded Fosite patch and test it with Iulita's selected modules."""
import difflib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parent.parent
SOURCE = ROOT / "third_party/fosite"
UPSTREAM = "github.com/ory/fosite@v0.49.0"
UPSTREAM_SUM = "h1:KNqO7RVt/1X8F08/UI0Y+GRvcpscCWgjqvpLBQPRovo="


def run(*args, cwd=ROOT, **kwargs):
    return subprocess.run(args, cwd=cwd, check=True, **kwargs)


def selected(path, root):
    return path.suffix == ".go" or path.relative_to(root).as_posix() in ("LICENSE", "go.mod", "go.sum")


def files(root):
    return {p.relative_to(root): p.read_bytes() for p in root.rglob("*") if p.is_file()}


def upstream_files():
    result = run("go", "mod", "download", "-json", UPSTREAM, capture_output=True, text=True)
    module = json.loads(result.stdout)
    if module["Sum"] != UPSTREAM_SUM:
        raise RuntimeError("Canonical Fosite source checksum changed")
    root = Path(module["Dir"])
    baseline = {p.relative_to(root): p.read_bytes() for p in root.rglob("*") if p.is_file() and selected(p, root)}
    # Public upstream test fixtures are explicitly annotated, preserving the
    # decoded PEM and test behavior. Reproduce these mechanical annotations here
    # so the migration patch need not duplicate a public private-key header or a
    # known wrong-password assignment that the simple commit hook would reject.
    annotations = [
        ("token/jwt/token_test.go", 'defaultPrivateKeyPEM = []byte(`\n-----BEGIN RSA PRIVATE KEY-----\n', # gitleaks:allow canonical public fixture reference
         'defaultPrivateKeyPEM = []byte("-----BEGIN RSA PRIVATE KEY-----\\n" + // gitleaks:allow canonical public test fixture, never a deployed key\n\t\t`'),
        ("integration/resource_owner_password_credentials_grant_test.go", 'password = "something-wrong"', # gitleaks:allow canonical intentionally invalid fixture reference
         'password = "something-wrong" // gitleaks:allow canonical intentionally invalid test credential'),
        ("introspect_test.go", 'req, _ := http.NewRequest("GET", "http://example.com/test?access_token="+token, nil)', # gitleaks:allow canonical public test expression
         'req, _ := http.NewRequest("GET", "http://example.com/test?access_token="+token, nil) // gitleaks:allow canonical public test expression'),
        ("introspection_request_handler.go", '//\ttoken=2YotnFZFEjr1zCsicMWpAA', # gitleaks:allow canonical public RFC example
         '//\ttoken=2YotnFZFEjr1zCsicMWpAA // gitleaks:allow canonical public RFC example'),
    ]
    for name, original, annotated in annotations:
        path = Path(name)
        text = baseline[path].decode()
        if text.count(original) != 1:
            raise RuntimeError("Canonical public fixture annotation is ambiguous: " + name)
        baseline[path] = text.replace(original, annotated).encode()
    return baseline


def patch_source(regenerate=False):
    baseline = upstream_files()
    patch = ROOT / "third_party/fosite.patch"
    if regenerate:
        current = files(SOURCE)
        lines = []
        for name in sorted(baseline.keys() | current.keys()):
            lines.extend(difflib.unified_diff(
                baseline.get(name, b"").decode().splitlines(keepends=True),
                current.get(name, b"").decode().splitlines(keepends=True),
                fromfile="a/" + str(name) if name in baseline else "/dev/null",
                tofile="b/" + str(name) if name in current else "/dev/null"))
        patch.write_text("".join(lines))
        return
    with tempfile.TemporaryDirectory(prefix="iulita-fosite-source-") as temporary:
        expected = Path(temporary)
        for name, data in baseline.items():
            target = expected / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
        run("git", "apply", str(patch), cwd=expected)
        if files(expected) != files(SOURCE):
            raise RuntimeError("Fosite source differs from the reviewed upstream patch")
    print("Canonical Fosite checksum, complete Go source/tests and bounded patch verified", flush=True)


def effective_modfile(temporary, module=None, legacy=False):
    modfile = Path(temporary) / "effective.mod"
    if module:
        # Begin with root's explicit requirements, retaining upstream-only test
        # dependencies at their declared versions. Promoting every transitive
        # graph node to a direct requirement can itself change lazy MVS selection.
        shutil.copyfile(ROOT / "go.mod", modfile)
        checksums = (SOURCE / "go.sum").read_text() + (ROOT / "go.sum").read_text()
        modfile.with_suffix(".sum").write_text(checksums)
        selected = run("go", "list", "-m", "-f", "{{if .Version}}{{.Path}}@{{.Version}}{{end}}", "all",
                       capture_output=True, text=True).stdout.splitlines()
        versions = dict(pin.split("@", 1) for pin in selected if pin)
        upstream = json.loads(run("go", "mod", "edit", "-json", cwd=SOURCE,
                                  capture_output=True, text=True).stdout)
        pins = ["-require=" + item["Path"] + "@" + versions.get(item["Path"], item["Version"])
                for item in upstream["Require"]]
        toolchain = run("go", "env", "GOVERSION", capture_output=True, text=True).stdout.strip()
        run("go", "mod", "edit", "-modfile=" + str(modfile), "-module=" + module,
            "-droprequire=github.com/ory/fosite", "-dropreplace=github.com/ory/fosite",
            "-toolchain=" + toolchain, *pins)
        return modfile
    shutil.copyfile(ROOT / "go.mod", modfile)
    shutil.copyfile(ROOT / "go.sum", modfile.with_suffix(".sum"))
    run("go", "mod", "edit", "-modfile=" + str(modfile), "-dropreplace=github.com/ory/fosite")
    if legacy:
        run("go", "mod", "edit", "-modfile=" + str(modfile),
            "-require=github.com/go-jose/go-jose/v3@v3.0.5")
    else:
        run("go", "mod", "edit", "-modfile=" + str(modfile),
            "-replace=github.com/ory/fosite=" + str(SOURCE))
    return modfile


def test_source(race=False):
    declared = json.loads(run("go", "mod", "edit", "-json", capture_output=True, text=True).stdout)
    shared = {item["Path"] for item in declared["Require"]}
    selected = run("go", "list", "-m", "-f", "{{if .Version}}{{.Path}}@{{.Version}}{{end}}", "all",
                   capture_output=True, text=True).stdout.splitlines()
    with tempfile.TemporaryDirectory(prefix="iulita-fosite-tests-") as temporary:
        modfile = effective_modfile(temporary, module="github.com/ory/fosite")
        args = ["go", "test", "-mod=mod", "-modfile=" + str(modfile), "-p", "2", "-count=1", "-timeout=5m"]
        if race:
            args.append("-race")
        run(*args, "./...", cwd=SOURCE)
        version = run("go", "list", "-modfile=" + str(modfile), "-m", "-f", "{{.Version}}",
                      "github.com/go-jose/go-jose/v4", capture_output=True, text=True).stdout.strip()
        if version != "v4.1.9":
            raise RuntimeError("Fosite suite used the wrong JOSE version: " + version)
        effective = set(run("go", "list", "-modfile=" + str(modfile), "-m", "-f",
                            "{{if .Version}}{{.Path}}@{{.Version}}{{end}}", "all",
                            capture_output=True, text=True).stdout.splitlines())
        for pin in selected:
            # Root's declared requirements include its linked dependencies.
            # Unused transitive graph metadata can differ when upstream test
            # dependencies are activated; it is not shipped application code.
            if pin and pin.split("@")[0] in shared and not pin.startswith("github.com/ory/fosite@") and pin not in effective:
                actual = [item for item in effective if item.startswith(pin.split("@")[0] + "@")]
                raise RuntimeError("Fosite tests changed an application-selected module: " + pin + " -> " + repr(actual))


def test_upgrade():
    # Build BOTH binaries first: authorization codes must not expire while the
    # second binary compiles. All keys/state are synthetic and deleted on exit.
    with tempfile.TemporaryDirectory(prefix="iulita-jose-upgrade-") as temporary:
        binaries = []
        for legacy in (True, False):
            directory = Path(temporary) / ("legacy" if legacy else "current")
            directory.mkdir()
            modfile = effective_modfile(directory, legacy=legacy)
            binary = directory / "oauthstore.test"
            run("go", "test", "-c", "-mod=mod", "-modfile=" + str(modfile), "-p", "2",
                "-o", str(binary), "./internal/mobile/oauthstore")
            graph = run("go", "list", "-mod=mod", "-modfile=" + str(modfile), "-deps",
                        "./internal/mobile/oauthstore", capture_output=True, text=True).stdout.splitlines()
            if ("github.com/go-jose/go-jose/v3" in graph) != legacy:
                raise RuntimeError("Upgrade fixture did not build the expected old/new JOSE dependency")
            binaries.append(binary)
        state = Path(temporary) / "state"
        state.mkdir(mode=0o700)
        for binary, phase in zip(binaries, ("issue", "resume")):
            env = dict(os.environ, IULITA_JOSE_UPGRADE_DIR=str(state), IULITA_JOSE_UPGRADE_PHASE=phase)
            run(str(binary), "-test.run=^TestJOSEUpgradeContinuity$", "-test.v", env=env)


def check_graph():
    # go list -m includes unused requirements inherited from ory/x. Inspect
    # linked package closures instead, including every production/helper command.
    (ROOT / "ui/dist").mkdir(parents=True, exist_ok=True)
    placeholder = ROOT / "ui/dist/.gitkeep"
    created = not placeholder.exists()
    try:
        if created:
            placeholder.touch()
        targets = [(None, None, "./cmd/...", None), ("linux", "amd64", "./cmd/...", "1")]
        for target_os, arch in (("linux", "amd64"), ("linux", "arm64"),
                                ("darwin", "arm64"), ("windows", "amd64")):
            targets.append((target_os, arch, "./cmd/iulita", "0"))
        for arch in ("amd64", "arm64"):
            targets.append(("linux", arch, "./cmd/ssh-worker", "0"))
        for target_os, arch, package, cgo in targets:
            env = dict(os.environ)
            if target_os:
                env.update(GOOS=target_os, GOARCH=arch, CGO_ENABLED=cgo)
            graph = run("go", "list", "-mod=readonly", "-deps", "-f",
                        "{{with .Module}}{{.Path}} {{.Version}}{{end}}", package,
                        env=env, capture_output=True, text=True).stdout.splitlines()
            if any(line.startswith("github.com/go-jose/go-jose/v3 ") for line in graph):
                raise RuntimeError(f"Vulnerable JOSE v3 in {target_os}/{arch} {package}")
            if package != "./cmd/ssh-worker" and "github.com/go-jose/go-jose/v4 v4.1.9" not in graph:
                raise RuntimeError(f"Expected official JOSE v4.1.9 missing from {target_os}/{arch} {package}")
        experiment = run("go", "list", "-mod=readonly", "-deps", "-test", "-f",
                         "{{with .Module}}{{.Path}} {{.Version}}{{end}}", "./...",
                         cwd=ROOT / "experiments/mobileoauth", capture_output=True, text=True).stdout.splitlines()
        if any(line.startswith("github.com/go-jose/go-jose/v3 ") for line in experiment):
            raise RuntimeError("Vulnerable JOSE v3 remains in the OAuth experiment")
        if "github.com/go-jose/go-jose/v4 v4.1.9" not in experiment:
            raise RuntimeError("OAuth experiment did not select official JOSE v4.1.9")
        print("All command and six release target closures checked; official JOSE v4.1.9 without v3", flush=True)
    finally:
        if created:
            placeholder.unlink()


if __name__ == "__main__":
    action = sys.argv[1] if len(sys.argv) == 2 else "verify"
    if action in ("verify", "regenerate"):
        patch_source(regenerate=action == "regenerate")
    elif action in ("test", "race"):
        test_source(race=action == "race")
    elif action == "upgrade":
        test_upgrade()
    elif action == "graph":
        check_graph()
    else:
        raise SystemExit("Expected verify, regenerate, test, race, upgrade or graph")
