import copy
import hashlib
from pathlib import Path
import re
from threading import Lock

from .schema import SchemaValidator, parse_json


def _local(name):
    return isinstance(name, str) and "\\" not in name and ":" not in name and all(part not in ("", ".", "..") for part in name.split("/"))


def _hash(value):
    return isinstance(value, str) and re.fullmatch(r"[a-f0-9]{64}", value)


class DataBundle:
    """Validated immutable snapshot. Read returns copies, never shared dictionaries."""

    @classmethod
    def open(cls, directory, expected_hash=""):
        root = Path(directory)
        seen = {"data_manifest.json"}

        def read(name):
            if not _local(name):
                raise ValueError("Invalid bundle path: %s" % name)
            current = root
            for part in name.split("/"):
                current = current / part
                if current.is_symlink():
                    raise ValueError("Symlink bundle path: " + name)
            if not current.is_file():
                raise ValueError("Not a regular bundle file: " + name)
            return current.read_bytes()

        def checked(entry):
            if not isinstance(entry, dict) or not _local(entry.get("path")) or not _hash(entry.get("sha256")) or entry["path"].lower() in seen:
                raise ValueError("Invalid or duplicate manifest file")
            seen.add(entry["path"].lower())
            data = read(entry["path"])
            if hashlib.sha256(data).hexdigest() != entry["sha256"]:
                raise ValueError("Checksum mismatch: " + entry["path"])
            return data

        manifest = parse_json(read("data_manifest.json").decode("utf-8"))
        if not isinstance(manifest, dict) or manifest.get("format") != "tabforge.data.v1" or not _hash(manifest.get("schemaHash")) or not isinstance(manifest.get("data"), list) or (expected_hash and expected_hash != manifest["schemaHash"]):
            raise ValueError("Invalid manifest or schema hash mismatch")
        checked(manifest["descriptor"])
        wire = parse_json(checked(manifest["wireSchema"]).decode("utf-8"))
        if not isinstance(wire, dict) or not isinstance(wire.get("messages"), dict) or not isinstance(wire.get("enums"), dict):
            raise ValueError("Invalid wire schema")
        validator = SchemaValidator(wire)
        values = {}
        for entry in manifest["data"]:
            data = checked(entry)
            if entry.get("message") not in wire["messages"]:
                raise ValueError("Unknown message: " + str(entry.get("message")))
            if entry.get("encoding") == "protojson":
                value = parse_json(data.decode("utf-8"))
                validator.validate(entry["message"], value)
                values[entry["path"]] = value
            elif entry.get("encoding") != "protobuf":
                raise ValueError("Unsupported encoding: " + str(entry.get("encoding")))
        result = cls()
        result._manifest, result._values, result._validator = manifest, values, validator
        return result

    @property
    def schema_hash(self):
        return self._manifest["schemaHash"]

    def entries(self):
        return copy.deepcopy(self._manifest["data"])

    def read(self, name, expected_message=""):
        entry = next((entry for entry in self._manifest["data"] if entry["path"] == name), None)
        if entry is None:
            raise ValueError("Data file not declared: " + name)
        if expected_message and entry["message"] != expected_message:
            raise ValueError("Message type mismatch")
        if entry["encoding"] != "protojson":
            raise ValueError("Python data loader supports ProtoJSON; use the matching .json entry")
        return copy.deepcopy(self._values[name])

    def decode(self, message, text):
        value = parse_json(text)
        self._validator.validate(message, value)
        return value


class DataStore:
    def __init__(self):
        self._snapshot = None
        self._lock = Lock()

    @property
    def snapshot(self):
        with self._lock:
            return self._snapshot

    def reload(self, directory, expected_hash=""):
        next_bundle = DataBundle.open(directory, expected_hash)
        with self._lock:
            self._snapshot = next_bundle
        return next_bundle
