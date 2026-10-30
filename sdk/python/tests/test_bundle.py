import hashlib
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor

from tabforge_data import DataBundle, DataStore
from tabforge_data.schema import MAX_JSON_DEPTH, MAX_JSON_NODES, parse_json, proto_bytes, proto_integer, proto_map_key

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
            missing = {key: value for key, value in manifest.items() if key != "descriptor"}
            manifest_path.write_text(json.dumps(missing), encoding="utf-8")
            with self.assertRaises(ValueError):
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


    def test_canonical_values_and_go_integer_rules(self):
        bundle = DataBundle.open(ROOT / "examples/complete/Generated")
        item = bundle.decode("tabforge.demo.config.Item", '{"id":"1e2","ownerId":18446744073709551615,"signedTotal":-9.223372036854775808e18,"rarity":1.0,"levels":[1.0,"2"],"iconHash":"AAF","priority":"7","price":"1.5","name":null,"extra":{"a":1.5e0}}')
        self.assertEqual(item, {"id": 100, "ownerId": "18446744073709551615", "signedTotal": "-9223372036854775808", "rarity": 1, "levels": [1, 2], "iconHash": "AAE=", "priority": 7, "price": 1.5, "extra": {"a": 1.5}})
        self.assertIs(type(item["extra"]["a"]), float)
        self.assertIs(type(item["price"]), float)
        self.assertEqual(bundle.decode("tabforge.demo.config.Tables", '{"byId":{"+1":{"id":1},"02":{}}}'), {"byId": {"1": {"id": 1}, "2": {}}})
        for text, signed, bits, expected in [("1.0", True, 32, "1"), ("100e-2", True, 32, "1"), ("-0.0", False, 64, "0"), ("1.5", True, 32, None), ("0.000000000000000000001e21", True, 64, None), ("1e+0002", True, 32, "100"), ("1e99999999999", True, 64, None), ("1.8446744073709551615e19", False, 64, "18446744073709551615"), ("١", True, 32, None)]:
            self.assertEqual(proto_integer(text, signed, bits), expected, text)
        self.assertEqual(proto_map_key("01", "int32"), "1")
        self.assertIsNone(proto_map_key("+1", "uint32"))
        self.assertEqual(proto_bytes("-_-_"), "+/+/")
        self.assertIsNone(proto_bytes("AA+_"))

    def test_data_budget(self):
        nested = lambda depth: "[" * depth + "0" + "]" * depth
        parse_json(nested(MAX_JSON_DEPTH))
        with self.assertRaises(ValueError):
            parse_json(nested(MAX_JSON_DEPTH + 1))
        with self.assertRaises(ValueError):
            parse_json("[" + "0," * 9 + "0]", max_nodes=10)
        self.assertEqual(MAX_JSON_NODES, 10000000)
        # Same fixture as databundle/cases_test.go: 20,000 rows, 180,002 JSON values.
        table = ("{\"items\":[" + ",".join('{"id":%d,"name":"item %d","ownerId":"%d","levels":[1,2,3],"price":1.5}' % (i, i, i) for i in range(1, 20001)) + "]}").encode("utf-8")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "Generated"
            shutil.copytree(ROOT / "examples/complete/Generated", root)
            (root / "data/tables.json").write_bytes(table)
            manifest_path = root / "data_manifest.json"
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            next(entry for entry in manifest["data"] if entry["path"] == "data/tables.json")["sha256"] = hashlib.sha256(table).hexdigest()
            manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
            tables = DataBundle.open(root).read("data/tables.json", "tabforge.demo.config.Tables")
            self.assertEqual(len(tables["items"]), 20000)
            self.assertEqual(tables["items"][-1]["ownerId"], "20000")


if __name__ == "__main__":
    unittest.main()
