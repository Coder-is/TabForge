import hashlib
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor

from tabforge_data import DataBundle, DataStore

ROOT = Path(__file__).resolve().parents[3]


class BundleTests(unittest.TestCase):
    def test_source_free_snapshot_and_failed_reload(self):
        with tempfile.TemporaryDirectory(prefix="tabforge Python 中文 ") as directory:
            root = Path(directory) / "Generated"
            shutil.copytree(ROOT / "examples/complete/Generated", root)
            store = DataStore()
            bundle = store.reload(root)
            tables = bundle.read("data/tables.json", "tabforge.demo.config.Tables")
            self.assertEqual(tables["items"][0]["ownerId"], "18446744073709551615")
            self.assertEqual(tables["items"][0]["signedTotal"], "-9223372036854775808")
            self.assertEqual(tables["items"][0]["unlockLevel"], 0)
            self.assertNotIn("unlockLevel", tables["items"][1])
            tables["items"][0]["name"] = "mutated"
            self.assertEqual(bundle.read("data/tables.json")["items"][0]["name"], "新手剑😀")
            with ThreadPoolExecutor(4) as executor:
                results = list(executor.map(lambda _: bundle.read("data/tables.json")["items"][0]["ownerId"], range(16)))
            self.assertEqual(len(set(results)), 1)
            for path, message in [("data/tables.pbb", ""), ("missing", ""), ("data/tables.json", "Wrong")]:
                with self.assertRaises(ValueError):
                    bundle.read(path, message)
            with self.assertRaises(ValueError):
                DataBundle.open(root, "0" * 64)
            invalid = b'{"unknown":1}'
            (root / "data/tables.json").write_bytes(invalid)
            with self.assertRaisesRegex(ValueError, "Checksum"):
                store.reload(root)
            self.assertIs(store.snapshot, bundle)
            manifest_path = root / "data_manifest.json"
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            next(entry for entry in manifest["data"] if entry["path"] == "data/tables.json")["sha256"] = hashlib.sha256(invalid).hexdigest()
            manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "declared field"):
                DataBundle.open(root)
            manifest["data"][0]["path"] = "../escape"
            manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
            with self.assertRaises(ValueError):
                DataBundle.open(root)

    def test_shared_wire_cases(self):
        bundle = DataBundle.open(ROOT / "examples/complete/Generated")
        cases = json.loads((ROOT / "examples/backend/cases.json").read_text(encoding="utf-8"))
        for case in cases:
            with self.subTest(case=case):
                if case["valid"]:
                    bundle.decode(case["message"], case["json"])
                else:
                    with self.assertRaises(ValueError):
                        bundle.decode(case["message"], case["json"])


if __name__ == "__main__":
    unittest.main()
