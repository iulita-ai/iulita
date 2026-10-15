#!/usr/bin/env python3
"""Generate pinned Go and JVM mobile contracts without installing global tools."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shlex
import shutil
import subprocess
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
LOCK = json.loads((ROOT / "api/mobile-toolchain.json").read_text())
CACHE = ROOT / "bin/mobile-proto-tools"
PROTO = ROOT / "api/proto"
GO_OUTPUT = ROOT / "api/gen/go"
JVM_OUTPUT = ROOT / "bin/mobile-proto/jvm"


def artifact(key, relative):
    spec = LOCK["artifacts"][key]
    path = CACHE / relative
    path.parent.mkdir(parents=True, exist_ok=True)
    if not path.exists() or hashlib.sha256(path.read_bytes()).hexdigest() != spec["sha256"]:
        with urllib.request.urlopen(spec["url"], timeout=60) as response:
            data = response.read()
        if hashlib.sha256(data).hexdigest() != spec["sha256"]:
            raise RuntimeError("SHA256 mismatch: " + key)
        temporary = path.with_suffix(path.suffix + ".tmp")
        temporary.write_bytes(data)
        temporary.replace(path)
    return path


def run(args, **kwargs):
    subprocess.run([str(a) for a in args], cwd=ROOT, check=True, **kwargs)


def setup():
    systems = {"Darwin": "osx", "Linux": "linux"}
    arches = {"arm64": "aarch_64", "aarch64": "aarch_64", "x86_64": "x86_64", "AMD64": "x86_64"}
    target = systems.get(platform.system(), "") + "-" + arches.get(platform.machine(), "")
    if "protoc/" + target not in LOCK["artifacts"]:
        raise RuntimeError("Unsupported toolchain platform: " + target)
    compiler = artifact("protoc/" + target, "protoc-" + LOCK["protoc_version"] + "/" + target + "/protoc")
    compiler.chmod(0o755)
    include = CACHE / ("include-" + LOCK["protoc_version"])
    for name in ("descriptor", "duration", "empty", "timestamp"):
        artifact("include/" + name + ".proto", include.relative_to(CACHE) / "google/protobuf" / (name + ".proto"))
    return compiler, include, target


def go_plugins():
    dest = CACHE / ("go-" + LOCK["protoc_gen_go"] + "-grpc-" + LOCK["protoc_gen_go_grpc"])
    dest.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GOBIN=str(dest))
    for binary, module, version in (
        ("protoc-gen-go", "google.golang.org/protobuf/cmd/protoc-gen-go", LOCK["protoc_gen_go"]),
        ("protoc-gen-go-grpc", "google.golang.org/grpc/cmd/protoc-gen-go-grpc", LOCK["protoc_gen_go_grpc"]),
    ):
        if not (dest / binary).exists():
            run(["go", "install", module + "@" + version], env=env)
    return dest


def generate_go(compiler, include, files, output):
    plugins = go_plugins()
    output.mkdir(parents=True, exist_ok=True)
    run([compiler, "-I" + str(PROTO), "-I" + str(include),
         "--plugin=protoc-gen-go=" + str(plugins / "protoc-gen-go"),
         "--plugin=protoc-gen-go-grpc=" + str(plugins / "protoc-gen-go-grpc"),
         "--go_out=" + str(output), "--go_opt=paths=source_relative",
         "--go-grpc_out=" + str(output), "--go-grpc_opt=paths=source_relative", *files])


def generate_jvm(compiler, include, target, files):
    java = shutil.which("java")
    if java is None:
        raise RuntimeError("JDK required for gRPC Kotlin generation")
    grpc_java = artifact("protoc-gen-grpc-java/" + target, "grpc-java-" + LOCK["grpc_java_version"] + "/" + target + "/protoc-gen-grpc-java")
    grpc_java.chmod(0o755)
    jar = artifact("grpc-kotlin", "grpc-kotlin-" + LOCK["grpc_kotlin_version"] + ".jar")
    wrapper = CACHE / "protoc-gen-grpc-kotlin"
    wrapper.write_text("#!/bin/sh\nexec " + shlex.quote(java) + " -jar " + shlex.quote(str(jar)) + '\n')
    wrapper.chmod(0o755)
    for part in ("java", "kotlin", "grpc-java", "grpc-kotlin"):
        (JVM_OUTPUT / part).mkdir(parents=True, exist_ok=True)
    run([compiler, "-I" + str(PROTO), "-I" + str(include),
         "--plugin=protoc-gen-grpc-java=" + str(grpc_java),
         "--plugin=protoc-gen-grpc-kotlin=" + str(wrapper),
         "--java_out=" + str(JVM_OUTPUT / "java"),
         "--kotlin_out=" + str(JVM_OUTPUT / "kotlin"),
         "--grpc-java_out=" + str(JVM_OUTPUT / "grpc-java"),
         "--grpc-kotlin_out=" + str(JVM_OUTPUT / "grpc-kotlin"), *files])
    print("JVM generated sources: " + str(JVM_OUTPUT.relative_to(ROOT)))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("go", "jvm", "all", "check"), default="all", nargs="?")
    mode = parser.parse_args().mode
    compiler, include, target = setup()
    files = [str(p.relative_to(PROTO)) for p in sorted((PROTO / "iulita/mobile/v1").glob("*.proto"))]
    if mode in ("go", "all", "check"):
        with tempfile.TemporaryDirectory(prefix="mobile-proto-") as tmp:
            output = Path(tmp)
            generate_go(compiler, include, files, output)
            expected = {p.relative_to(output): p for p in output.rglob("*.pb.go")}
            actual = {p.relative_to(GO_OUTPUT): p for p in GO_OUTPUT.rglob("*.pb.go")}
            if mode == "check":
                changed = set(expected) ^ set(actual)
                changed |= {p for p in set(expected) & set(actual) if expected[p].read_bytes() != actual[p].read_bytes()}
                if changed:
                    raise RuntimeError("Generated Go drift: " + ", ".join(str(p) for p in sorted(changed)))
                print("Generated Go contracts match pinned toolchain")
            else:
                for p in set(actual) - set(expected):
                    actual[p].unlink()
                for name, source in expected.items():
                    dest = GO_OUTPUT / name
                    dest.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copyfile(source, dest)
                print("Go generated sources: " + str(GO_OUTPUT.relative_to(ROOT)))
    if mode in ("jvm", "all"):
        generate_jvm(compiler, include, target, files)


if __name__ == "__main__":
    main()
