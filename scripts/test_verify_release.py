"""Integrity checks must reject incomplete and modified delivery inventories."""
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from verify_release import integrity


class IntegrityTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.out = Path(self.temp.name)
        self.data = b"example package bytes"
        (self.out / "package.zip").write_bytes(self.data)
        artifact = {"path": "package.zip", "size": len(self.data),
                    "sha256": hashlib.sha256(self.data).hexdigest()}
        self.manifest = {"format": "tabforge.release.v1", "artifacts": [artifact]}
        self.seal()

    def seal(self):
        (self.out / "release.json").write_text(json.dumps(self.manifest), encoding="utf-8")
        paths = sorted(p for p in self.out.iterdir() if p.name != "SHA256SUMS")
        (self.out / "SHA256SUMS").write_text("".join(
            hashlib.sha256(p.read_bytes()).hexdigest() + "  " + p.name + "\n" for p in paths), encoding="utf-8")

    def test_complete_delivery(self):
        self.assertEqual(integrity(self.out), {"package.zip", "release.json"})

    def test_modified_file(self):
        (self.out / "package.zip").write_bytes(b"modified")
        with self.assertRaises(ValueError):
            integrity(self.out)

    def test_missing_file(self):
        (self.out / "package.zip").unlink()
        with self.assertRaises(ValueError):
            integrity(self.out)

    def test_extra_file(self):
        (self.out / "unexpected.zip").write_bytes(b"extra")
        with self.assertRaises(ValueError):
            integrity(self.out)

    def test_manifest_cannot_omit_package_even_with_new_checksums(self):
        self.manifest["artifacts"] = []
        self.seal()
        with self.assertRaises(ValueError):
            integrity(self.out)

    def test_duplicate_checksum_entry(self):
        path = self.out / "SHA256SUMS"
        path.write_text(path.read_text() * 2)
        with self.assertRaises(ValueError):
            integrity(self.out)

    def test_checksum_path_cannot_escape_delivery(self):
        (self.out / "SHA256SUMS").write_text("0" * 64 + "  ../outside.zip\n")
        with self.assertRaises(ValueError):
            integrity(self.out)


if __name__ == "__main__":
    unittest.main()
