"""Verify delivery integrity and exercise unpacked tools on the current host."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import struct
import subprocess
import tempfile
import xml.etree.ElementTree as ET
import zipfile

TARGETS = ("darwin-arm64", "darwin-x64", "win32-x64", "win32-arm64")
KINDS = ("project", "vscode", "unity", "cocos", "godot")


def archive_name(kind, target):
    return ("tabforge-" + target + ".vsix" if kind == "vscode"
            else "tabforge-" + kind + "-" + target + ".zip")


def digest(path):
    checksum = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            checksum.update(chunk)
    return checksum.hexdigest()


def integrity(out):
    seen = set()
    for line in (out / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
        expected, name = line.split("  ", 1)
        if name in seen or Path(name).name != name or "\\" in name:
            raise ValueError("Unsafe or duplicate checksum path: " + name)
        seen.add(name)
        path = out / name
        if path.is_symlink() or not path.is_file() or digest(path) != expected:
            raise ValueError("Checksum mismatch: " + name)
    actual = {p.name for p in out.iterdir() if p.name != "SHA256SUMS"}
    if seen != actual:
        raise ValueError("Checksum inventory differs from delivery contents")
    manifest_path = out / "release.json"
    if manifest_path.exists():
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        if manifest["format"] != "tabforge.release.v1":
            raise ValueError("Unknown release manifest")
        entries = manifest["artifacts"]
        if len({a["path"] for a in entries}) != len(entries):
            raise ValueError("Duplicate release artifact")
        if {a["path"] for a in entries} != seen - {"release.json"}:
            raise ValueError("Release manifest inventory differs from checksums")
        for artifact in entries:
            path = out / artifact["path"]
            if path.stat().st_size != artifact["size"] or digest(path) != artifact["sha256"]:
                raise ValueError("Release manifest mismatch: " + artifact["path"])
    return seen


def check_archive(path, kind, target, version):
    with zipfile.ZipFile(path) as archive:
        if archive.testzip() is not None:
            raise ValueError("Corrupt archive: " + path.name)
        names = archive.namelist()
        if len(names) != len(set(names)):
            raise ValueError("Duplicate ZIP members: " + path.name)
        for name in names:
            parts = PurePosixPath(name)
            if parts.is_absolute() or ".." in parts.parts or "\\" in name or ":" in name:
                raise ValueError("Unsafe ZIP member: " + name)
            mode = archive.getinfo(name).external_attr >> 16
            if mode & 0o170000 == 0o120000:
                raise ValueError("ZIP symlink: " + name)
        binaries = [n for n in names if n.endswith("/tabforge") or n.endswith("/tabforge.exe")]
        if len(binaries) != 1:
            raise ValueError("Expected one bundled tool: " + path.name)
        data = archive.read(binaries[0])
        if target.startswith("darwin"):
            cpu = 0x0100000C if target.endswith("arm64") else 0x01000007
            if data[:4] != b"\xcf\xfa\xed\xfe" or struct.unpack_from("<I", data, 4)[0] != cpu:
                raise ValueError("Mach-O architecture mismatch: " + path.name)
            if not (archive.getinfo(binaries[0]).external_attr >> 16) & 0o111:
                raise ValueError("Tool executable permission missing")
        else:
            if data[:2] != b"MZ":
                raise ValueError("Expected Windows PE executable")
            offset = struct.unpack_from("<I", data, 0x3C)[0]
            cpu = 0xAA64 if target.endswith("arm64") else 0x8664
            if data[offset:offset + 4] != b"PE\0\0" or struct.unpack_from("<H", data, offset + 4)[0] != cpu:
                raise ValueError("PE architecture mismatch: " + path.name)
        if not any("THIRD_PARTY_LICENSES/" in n for n in names):
            raise ValueError("Third party licenses missing")
        roots = {"project": "TabForgeProject", "vscode": "extension", "unity": "com.tabforge.editor",
                 "cocos": "tabforge", "godot": "addons/tabforge"}
        root = roots[kind]
        if root + "/LICENSE" not in names:
            raise ValueError("Package license missing")
        if kind in ("vscode", "unity", "cocos"):
            package = json.loads(archive.read(root + "/package.json"))
            if package["version"] != version:
                raise ValueError("Plugin version mismatch: " + path.name)
        if kind == "vscode":
            manifest = ET.fromstring(archive.read("extension.vsixmanifest"))
            identity = next(e for e in manifest.iter() if e.tag.endswith("Identity"))
            if identity.attrib["Version"] != version or identity.attrib["TargetPlatform"] != target:
                raise ValueError("VSIX identity mismatch")
        if kind == "godot" and ('version="' + version + '"') not in archive.read(root + "/plugin.cfg").decode("utf-8"):
            raise ValueError("Godot version mismatch")
        if kind == "project":
            manifest = json.loads(archive.read(root + "/Generated/data_manifest.json"))
            if manifest["format"] != "tabforge.data.v1" or not manifest["data"]:
                raise ValueError("Generated data manifest missing")
        return binaries[0]


def extract(path, destination):
    # check_archive validates every member before this function is used.
    with zipfile.ZipFile(path) as archive:
        archive.extractall(destination)
        for entry in archive.infolist():
            if not entry.is_dir() and os.name != "nt":
                (destination / entry.filename).chmod((entry.external_attr >> 16) & 0o777 or 0o644)


def snapshot(path):
    return {str(p.relative_to(path)): digest(p) for p in path.rglob("*") if p.is_file()}


def native_check(out, target, info):
    checks = []
    with tempfile.TemporaryDirectory(prefix="TabForge install 中文 ") as temp:
        work = Path(temp)
        env = os.environ.copy()
        env["PATH"] = ""

        def run(tool, *args, success=True):
            result = subprocess.run([str(tool), *[str(a) for a in args]], cwd=work, env=env,
                                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                    text=True, encoding="utf-8", timeout=90)
            if (result.returncode == 0) != success:
                raise RuntimeError(result.stdout)
            return result.stdout

        for kind in KINDS:
            dest = work / (kind + " install 中文 $(literal)")
            dest.mkdir()
            path = out / archive_name(kind, target)
            binary = check_archive(path, kind, target, info["version"])
            extract(path, dest)
            tool = dest / binary
            text = run(tool, "-version")
            commit = info["gitCommit"] + ("-dirty" if info["dirty"] else "")
            fields = dict(line.split(":", 1) for line in text.splitlines() if ":" in line)
            for key, expected in (("Version", info["version"]), ("GitCommit", commit), ("BuildTime", info["buildTime"])):
                if fields.get(key, "").strip() != expected:
                    raise ValueError("Tool provenance mismatch: " + kind)
            if kind == "project":
                project = dest / "TabForgeProject"
                generated = project / "Generated"
                resource = generated
                args = ["-project=" + str(project), "-report"]
                run(tool, *args)
            else:
                game = dest / "game"
                game.mkdir()
                project = game / "TabForge"
                args = ["-project=" + str(project), "-report"]
                init = ["-init=" + str(project), "-report"]
                if kind in ("unity", "cocos", "godot"):
                    if kind == "unity":
                        (game / "Assets").mkdir()
                        generated = game / "Assets/TabForgeGenerated"
                        resource = generated / "Resources/TabForge"
                    elif kind == "cocos":
                        (game / "assets").mkdir()
                        generated = game / "assets/resources/tabforge"
                        resource = generated
                    else:
                        (game / "project.godot").write_text("config_version=5\n", encoding="utf-8")
                        generated = game / "tabforge_generated"
                        resource = generated
                    editor = ["-editor=" + kind, "-editor_project=" + str(game)]
                    args.extend(editor)
                    init.extend(editor)
                else:
                    generated = project / "Generated"
                    resource = generated
                run(tool, *init)
                run(tool, *args)
            tables = json.loads((resource / "data/tables.json").read_text(encoding="utf-8"))
            if tables["items"][0]["ownerId"] != "18446744073709551615":
                raise ValueError("Exported data changed")
            before = snapshot(generated)
            run(tool, *args, "-check")
            if snapshot(generated) != before:
                raise ValueError("Check modified generated data")
            bad = project / "Protocols/bad.proto"
            bad.write_text('syntax = "proto3";\nmessage Broken { string name = ; }', encoding="utf-8")
            run(tool, *args, success=False)
            if snapshot(generated) != before:
                raise ValueError("Failed export replaced valid data")
            report_root = project if kind in ("project", "vscode") else game
            report = json.loads((report_root / ".tabforge-report.json").read_text(encoding="utf-8"))
            if report["success"] or not any(d["code"] == "proto_compile" for d in report["diagnostics"]):
                raise ValueError("Expected Proto error report")
            checks.append(kind + ": version, export/import, check, failure preservation, empty PATH, Unicode/spaced paths")
    return checks


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--out", required=True)
    parser.add_argument("--target", choices=("all", *TARGETS), default=None)
    parser.add_argument("--integrity-only", action="store_true", help="read-only check of a sealed delivery")
    args = parser.parse_args()
    out = Path(args.out).resolve()
    files = integrity(out)
    if args.integrity_only:
        if "release.json" not in files:
            raise ValueError("Integrity-only verification requires a sealed release.json")
        print("Delivery checksums and manifest verified:", out)
        return
    info = json.loads((out / "build-info.json").read_text(encoding="utf-8"))
    selected = args.target or "all"
    targets = TARGETS if selected == "all" else (selected,)
    expected = {archive_name(kind, target) for kind in KINDS for target in targets}
    actual = {n for n in files if n.endswith(".vsix") or (n.endswith(".zip") and not n.startswith("tabforge-go-"))}
    if actual != expected:
        raise ValueError("Client package inventory differs from selected targets")
    for target in targets:
        for kind in KINDS:
            check_archive(out / archive_name(kind, target), kind, target, info["version"])
    machine = platform.machine().lower()
    arch = "arm64" if machine in ("arm64", "aarch64") else "x64" if machine in ("x86_64", "amd64") else ""
    system = {"Darwin": "darwin", "Windows": "win32"}.get(platform.system(), "")
    native = system + "-" + arch
    checks = native_check(out, native, info) if native in targets else []
    report = {
        "format": "tabforge.client.validation.v1", "version": info["version"],
        "archivesChecked": len(expected), "targets": list(targets),
        "nativeTarget": native if checks else None, "nativeChecks": checks,
        "limits": ["Other targets were checked structurally; their tools were not executed.",
                   "Editor UI installation, window interactions and target device builds were not exercised.",
                   "macOS development tools are unsigned; downloaded-file quarantine was not exercised.",
                   "Double-click shell/batch entry points were not exercised."],
    }
    (out / "client-validation.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
