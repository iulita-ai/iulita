"""Publication must fail before any Docker call for untrusted archive receipts."""
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import subprocess
import tarfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("security", Path(__file__).with_name("container_security.py"))
security = importlib.util.module_from_spec(spec)
spec.loader.exec_module(security)


class ArchiveIdentityTests(unittest.TestCase):
    def archive(self, directory, manifests):
        path = Path(directory) / "image.tar"
        config = b'{"architecture":"amd64","os":"linux","rootfs":{"diff_ids":[]}}'
        with tarfile.open(path, "w") as archive:
            for name, data in [("manifest.json", json.dumps(manifests).encode()), ("config.json", config)]:
                member = tarfile.TarInfo(name)
                member.size = len(data)
                archive.addfile(member, io.BytesIO(data))
        return path, config

    def test_config_identity_comes_from_archive_bytes_not_store_manifest_id(self):
        with tempfile.TemporaryDirectory() as directory:
            path, raw = self.archive(directory, [{"Config": "config.json", "RepoTags": ["local/fixture:checked"], "Layers": []}])
            identity, config = security.archive_config(path)
            self.assertEqual(identity, "sha256:" + hashlib.sha256(raw).hexdigest())
            self.assertEqual(config["architecture"], "amd64")

    def test_multiple_images_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            path, _ = self.archive(directory, [{"Config": "config.json"}, {"Config": "config.json"}])
            with self.assertRaisesRegex(RuntimeError, "Ambiguous image archive"):
                security.archive_config(path)


class PublicationBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.env = {"GITHUB_REF": "refs/tags/v0.56.2", "GITHUB_EVENT_NAME": "push", "GITHUB_SHA": "a" * 40,
                    "GITHUB_RUN_ID": "42", "GITHUB_RUN_ATTEMPT": "1", "GITHUB_REPOSITORY": "owner/project"}
        for role, arch in security.EXPECTED:
            directory = self.root / (role + "-" + arch)
            directory.mkdir()
            data = b"isolated archive fixture"
            (directory / "image.tar").write_bytes(data)
            (directory / "receipt.json").write_text(json.dumps({"role": role, "arch": arch, "image_id": "sha256:" + hashlib.sha256((role + arch).encode()).hexdigest(),
                "archive_sha256": hashlib.sha256(data).hexdigest(), "source_sha": self.env["GITHUB_SHA"], "run_id": "42",
                "run_attempt": "1", "scanner": security.SCANNER, "gate": "passed"}))

    def denied(self, message):
        with patch.dict(os.environ, self.env), patch.object(security, "run") as docker:
            with self.assertRaisesRegex(RuntimeError, message):
                security.publish(str(self.root), "v0.56.2")
            docker.assert_not_called()

    def first(self):
        return next(self.root.glob("*/receipt.json"))

    def test_pull_request_cannot_publish(self):
        self.env["GITHUB_EVENT_NAME"] = "pull_request"
        self.denied("Only tag pushes")

    def test_missing_platform_cannot_publish(self):
        self.first().unlink()
        self.denied("Five checked archives")

    def test_other_source_run_or_attempt_cannot_publish(self):
        for field in ["source_sha", "run_id", "run_attempt"]:
            with self.subTest(field=field):
                path = self.first()
                original = json.loads(path.read_text())
                modified = dict(original, **{field: "untrusted"})
                path.write_text(json.dumps(modified))
                self.denied("another source/run/attempt")
                path.write_text(json.dumps(original))

    def test_changed_archive_cannot_publish(self):
        (self.first().parent / "image.tar").write_bytes(b"tampered")
        self.denied("Archive checksum changed")

    def test_missing_gate_or_wrong_scanner_cannot_publish(self):
        for field in ["gate", "scanner"]:
            with self.subTest(field=field):
                path = self.first()
                original = json.loads(path.read_text())
                path.write_text(json.dumps(dict(original, **{field: "untrusted"})))
                self.denied("Missing trusted scan")
                path.write_text(json.dumps(original))

    def exercise(self, fail=None, mismatch=False):
        self.commands = []
        receipts = [json.loads(path.read_text()) for path in self.root.glob("*/receipt.json")]
        def docker(*args, **kwargs):
            self.commands.append(args)
            if fail and fail(args):
                raise subprocess.CalledProcessError(1, args)
            if args[1:3] == ("image", "inspect"):
                receipt = next(row for row in receipts if row["image_id"] == args[3])
                return json.dumps([{"Id": receipt["image_id"], "Os": "linux", "Architecture": "wrong" if mismatch else receipt["arch"]}])
            if args[1] == "push":
                return "digest: sha256:" + hashlib.sha256(args[2].encode()).hexdigest()
            if args[1:4] == ("buildx", "imagetools", "inspect"):
                return "Digest: sha256:" + "d" * 64
            return ""
        with patch("builtins.print"), patch.dict(os.environ, self.env), patch.object(security, "run", side_effect=docker), \
             patch.object(security.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "", "manifest not found")):
            security.publish(str(self.root), "v0.56.2")

    def test_all_platforms_push_before_numeric_tags_and_app_is_last(self):
        self.exercise()
        pushes = [i for i, args in enumerate(self.commands) if args[1] == "push"]
        promotions = [(i, args) for i, args in enumerate(self.commands) if args[1:4] == ("buildx", "imagetools", "create") and args[5].endswith(":0.56.2")]
        self.assertEqual(len(pushes), 5)
        self.assertEqual(len(promotions), 3)
        self.assertLess(max(pushes), min(i for i, _ in promotions))
        self.assertEqual([args[5] for _, args in promotions], ["ghcr.io/owner/project-ssh-worker:0.56.2", "ghcr.io/owner/project-mobile-gateway:0.56.2", "ghcr.io/owner/project:0.56.2"])
        for _, args in promotions:
            self.assertTrue(all("@sha256:" in child for child in args[6:]))
        self.assertEqual(json.loads((self.root / "published.json").read_text())["phase"], "complete")

    def test_staging_failure_never_promotes_numeric_tags(self):
        with self.assertRaises(subprocess.CalledProcessError):
            self.exercise(fail=lambda args: args[1] == "push" and "ssh-worker" in args[2])
        self.assertFalse(any(args[1:4] == ("buildx", "imagetools", "create") for args in self.commands))
        self.assertTrue((self.root / "published.json").is_file())

    def test_loaded_platform_mismatch_never_pushes(self):
        with self.assertRaisesRegex(RuntimeError, "identity/platform differs"):
            self.exercise(mismatch=True)
        self.assertFalse(any(args[1] == "push" for args in self.commands))

    def test_late_failure_keeps_numeric_publication_receipt(self):
        def fail(args):
            return args[1:4] == ("buildx", "imagetools", "create") and "ghcr.io/owner/project:latest" in args
        with self.assertRaises(subprocess.CalledProcessError):
            self.exercise(fail=fail)
        journal = json.loads((self.root / "published.json").read_text())
        self.assertEqual(journal["images"]["app"]["state"], "numeric_published")
        self.assertEqual(journal["images"]["app"]["digest"], "sha256:" + "d" * 64)


if __name__ == "__main__":
    unittest.main()
