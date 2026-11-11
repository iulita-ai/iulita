#!/usr/bin/env python3
"""Scan archives without credentials; publish only the exact checked artifacts."""
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys

SCANNER = "ghcr.io/aquasecurity/trivy@sha256:8f6bb947dbc528902e11f2f5be52f7b519f6a39d5d148f41166f2040499768fb"
EXPECTED = {("app", "amd64"), ("ssh-worker", "amd64"), ("ssh-worker", "arm64"),
            ("mobile-gateway", "amd64"), ("mobile-gateway", "arm64")}


def run(*args, **kwargs):
    return subprocess.check_output(args, text=True, timeout=1800, **kwargs).strip()


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def sha256(path):
    with path.open("rb") as handle:
        return hashlib.file_digest(handle, "sha256").hexdigest()


def scan(role, arch, image, output):
    require((role, arch) in EXPECTED, "Unexpected image/platform")
    output = Path(output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    reports = output / "reports"
    reports.mkdir(exist_ok=True)
    archive = output / "image.tar"
    inspect = json.loads(run("docker", "image", "inspect", image))[0]
    require(inspect["Os"] == "linux" and inspect["Architecture"] == arch, "Wrong local image platform")
    run("docker", "save", "-o", str(archive), image)

    # Scanner sees only this archive and its own output/cache. No Docker socket,
    # Git checkout, credentials, user home or fixture private keys are mounted.
    command = ["docker", "run", "--rm", "--read-only", "--cap-drop=ALL",
               "--security-opt=no-new-privileges", "--user", f"{os.getuid()}:{os.getgid()}",
               "--tmpfs", "/tmp:rw,noexec,nosuid,size=512m", "--memory=3g", "--cpus=2",
               "-v", f"{archive}:/input/image.tar:ro", "-v", f"{reports}:/output:rw", SCANNER,
               "--cache-dir", "/output/cache", "image", "--input", "/input/image.tar",
               "--scanners", "vuln", "--ignorefile", "", "--timeout", "15m"]
    run(*command, "--format", "json", "--list-all-pkgs", "--output", "/output/vulnerabilities.json", "--exit-code", "0")
    report = json.loads((reports / "vulnerabilities.json").read_text())
    require(report.get("Metadata", {}).get("ImageID") == inspect["Id"], "Scanner inspected a different image")
    require(report.get("Metadata", {}).get("OS", {}).get("Family") == "alpine", "OS was not identified")
    require(str(report["Metadata"]["OS"].get("Name", "")).startswith("3.24"), "Unexpected or unsupported Alpine branch")
    require(not report["Metadata"]["OS"].get("EOSL", False), "Alpine branch is no longer supported")
    results = report.get("Results", [])
    os_packages = [pkg for result in results if result.get("Class") == "os-pkgs" for pkg in result.get("Packages", [])]
    require(os_packages and any(pkg.get("Name") == "musl" for pkg in os_packages), "OS inventory is incomplete")
    if role in ("app", "ssh-worker"):
        binary = "iulita" if role == "app" else "ssh-worker"
        go_results = [result for result in results if result.get("Type") == "gobinary"
                      and re.search(r"(^|/)" + binary + r"(?: \(.*\))?$", result.get("Target", ""))]
        require(go_results, "Expected Go binary was not analyzed")
        stdlib = [pkg for result in go_results for pkg in result.get("Packages", []) if pkg.get("Name") == "stdlib"]
        require(stdlib and all(pkg.get("Version", "").removeprefix("go").removeprefix("v") == "1.27.4" for pkg in stdlib), "Expected Go standard library inventory is missing or outdated")
    database = json.loads((reports / "cache/db/metadata.json").read_text())
    updated = datetime.datetime.fromisoformat(database["UpdatedAt"].replace("Z", "+00:00"))
    age = datetime.datetime.now(datetime.timezone.utc) - updated
    require(datetime.timedelta(hours=-1) <= age <= datetime.timedelta(hours=72), "Vulnerability database is stale or future-dated")
    (reports / "database.json").write_text(json.dumps(database, indent=2) + "\n")
    # Freeze the already freshness-checked database across report/SBOM/gate.
    command += ["--skip-db-update", "--skip-java-db-update"]
    database_digest = sha256(reports / "cache/db/metadata.json")
    run(*command, "--format", "cyclonedx", "--output", "/output/sbom.cdx.json", "--exit-code", "0")
    sbom = json.loads((reports / "sbom.cdx.json").read_text())
    require(sbom.get("bomFormat") == "CycloneDX" and sbom.get("components"), "SBOM is empty or invalid")
    with (reports / "gate.txt").open("w") as handle:
        # Known unfixed findings remain in the full JSON. Available fixes for
        # HIGH/CRITICAL vulnerabilities are mandatory; scanner errors also fail.
        subprocess.run(command + ["--severity", "HIGH,CRITICAL", "--ignore-unfixed", "--exit-code", "1"],
                       text=True, stdout=handle, stderr=subprocess.STDOUT, timeout=1800, check=True)
    require(sha256(reports / "cache/db/metadata.json") == database_digest, "Database changed between security outputs")
    receipt = {"role": role, "arch": arch, "platform": "linux/" + arch, "image_id": inspect["Id"],
               "archive_sha256": sha256(archive), "source_sha": os.environ["GITHUB_SHA"],
               "run_id": os.environ["GITHUB_RUN_ID"], "run_attempt": os.environ["GITHUB_RUN_ATTEMPT"],
               "scanner": SCANNER, "gate": "passed", "database_updated_at": database["UpdatedAt"]}
    (output / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print(json.dumps(receipt))


def publish(directory, version):
    require(re.fullmatch(r"v0\.\d+\.\d+", version), "A stable version tag is required")
    require(os.environ["GITHUB_REF"] == "refs/tags/" + version and os.environ["GITHUB_EVENT_NAME"] == "push", "Only tag pushes may publish")
    directory = Path(directory).resolve()
    receipts = list(directory.glob("*/receipt.json"))
    require(len(receipts) == len(EXPECTED), "Five checked archives are required")
    verified = {}
    for path in receipts:
        receipt = json.loads(path.read_text())
        key = (receipt["role"], receipt["arch"])
        require(key in EXPECTED and key not in verified, "Unexpected or duplicate image receipt")
        for field, env in [("source_sha", "GITHUB_SHA"), ("run_id", "GITHUB_RUN_ID"), ("run_attempt", "GITHUB_RUN_ATTEMPT")]:
            require(receipt[field] == os.environ[env], "Receipt belongs to another source/run/attempt")
        require(receipt["gate"] == "passed" and receipt["scanner"] == SCANNER, "Missing trusted scan gate")
        require(re.fullmatch(r"sha256:[0-9a-f]{64}", receipt["image_id"]), "Malformed image identity")
        archive = path.parent / "image.tar"
        require(sha256(archive) == receipt["archive_sha256"], "Archive checksum changed after scanning")
        verified[key] = dict(receipt, archive=archive)
    require(set(verified) == EXPECTED, "Image/platform set is incomplete")
    for receipt in verified.values():
        archive = receipt["archive"]
        run("docker", "load", "-i", str(archive))
        loaded = json.loads(run("docker", "image", "inspect", receipt["image_id"]))[0]
        require(loaded["Id"] == receipt["image_id"] and loaded["Os"] == "linux" and loaded["Architecture"] == receipt["arch"], "Loaded archive identity/platform differs")
    registry = "ghcr.io/" + os.environ["GITHUB_REPOSITORY"].lower()
    repos = {"app": registry, "ssh-worker": registry + "-ssh-worker", "mobile-gateway": registry + "-mobile-gateway"}
    number = version[1:]
    publication = {"version": version, "source_sha": os.environ["GITHUB_SHA"], "run_id": os.environ["GITHUB_RUN_ID"],
                   "run_attempt": os.environ["GITHUB_RUN_ATTEMPT"], "phase": "validated", "staging": {}, "images": {}}
    def journal():
        temporary = directory / "published.json.tmp"
        temporary.write_text(json.dumps(publication, indent=2) + "\n")
        temporary.replace(directory / "published.json")
    journal()
    digests = {}
    # Push only run-specific staging tags. No semver is visible until every
    # checked platform has uploaded successfully. Capture each actual push digest.
    for (role, arch), receipt in sorted(verified.items()):
        repo = repos[role]
        staging = repo + ":ci-" + "-".join([receipt["source_sha"], receipt["run_id"], receipt["run_attempt"], arch])
        publication["staging"][role + "/" + arch] = {"tag": staging, "image_id": receipt["image_id"], "state": "push_pending"}
        journal()
        run("docker", "tag", receipt["image_id"], staging)
        result = run("docker", "push", staging, stderr=subprocess.STDOUT)
        found = re.findall(r"digest: (sha256:[0-9a-f]{64})", result)
        require(len(found) == 1, "Exact pushed manifest digest unavailable")
        digests.setdefault(role, []).append(repo + "@" + found[0])
        publication["staging"][role + "/" + arch].update(digest=found[0], state="pushed")
        journal()
    # A new image package now exists after its first checked staging upload.
    # Establish absence while authenticated; permission/network failures are not
    # interpreted as permission to overwrite an immutable release version.
    for repo in repos.values():
        existing = subprocess.run(["docker", "buildx", "imagetools", "inspect", repo + ":" + number], capture_output=True, text=True, timeout=120)
        require(existing.returncode != 0, "Release image already exists; use a new patch version")
        require(any(x in existing.stderr.lower() for x in ["not found", "manifest unknown", "name unknown"]), "Could not establish that release image tag is absent")
    publication["phase"] = "staged"
    journal()
    for role in ["ssh-worker", "mobile-gateway", "app"]:
        repo = repos[role]
        publication["images"][role] = {"image": repo + ":" + number, "children": digests[role], "state": "numeric_promotion_pending"}
        journal()
        run("docker", "buildx", "imagetools", "create", "--tag", repo + ":" + number, *digests[role])
        description = run("docker", "buildx", "imagetools", "inspect", repo + ":" + number)
        digest = re.search(r"^Digest:\s+(sha256:[0-9a-f]{64})$", description, re.MULTILINE)
        require(digest is not None, "Published manifest digest unavailable")
        publication["images"][role].update(digest=digest.group(1), state="numeric_published")
        journal()
        # Preserve the established convenience tags, referencing the exact
        # published immutable manifest instead of another build or staging tag.
        minor = number.rsplit(".", 1)[0]
        run("docker", "buildx", "imagetools", "create", "--tag", repo + ":" + minor,
            "--tag", repo + ":latest", repo + "@" + digest.group(1))
        publication["images"][role]["state"] = "published"
        journal()
    publication["phase"] = "complete"
    journal()
    print(json.dumps(publication))


if __name__ == "__main__":
    if sys.argv[1] == "scan":
        scan(*sys.argv[2:])
    elif sys.argv[1] == "publish":
        publish(*sys.argv[2:])
    else:
        raise SystemExit("Expected scan or publish")
