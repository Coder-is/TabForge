"""Read-only publication checks and a plan; never uploads or reads credentials."""
import argparse
import json
from pathlib import Path
import subprocess

from verify_release import integrity


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--out", required=True)
    parser.add_argument("--plan", help="write outside the sealed delivery; otherwise print JSON")
    args = parser.parse_args()
    out = Path(args.out).resolve()
    integrity(out)
    manifest = json.loads((out / "release.json").read_text(encoding="utf-8"))
    if manifest["dirty"]:
        raise ValueError("Publication requires a clean committed build")
    root = Path(__file__).resolve().parents[1]
    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
    if commit != manifest["gitCommit"]:
        raise ValueError("Delivery was built from a different commit")
    version = manifest["version"]
    package = json.loads((root / "sdk/typescript/package.json").read_text(encoding="utf-8"))
    plugin = json.loads((root / "editors/vscode/package.json").read_text(encoding="utf-8"))
    if package["version"] != version or plugin["version"] != version:
        raise ValueError("Source versions differ from delivery")
    plan = {
        "format": "tabforge.publish.plan.v1", "version": version, "gitCommit": commit,
        "githubTag": "v" + version, "uploadsExecuted": False,
        "channels": {
            "npm": {"name": package["name"], "requires": ["ownership of @tabforge scope", "npm login or configured trusted publisher"],
                    "command": ["npm", "publish", str(out / ("tabforge-protocol-runtime-" + version + ".tgz")), "--access", "public", "--tag", "next"]},
            "pypi": {"name": "tabforge-data", "requires": ["ownership of package name", "PyPI trusted publisher or scoped upload token"],
                     "checkCommand": ["python", "-m", "twine", "check", str(out / ("tabforge_data-" + version + "-py3-none-any.whl"))],
                     "command": ["python", "-m", "twine", "upload", str(out / ("tabforge_data-" + version + "-py3-none-any.whl"))]},
            "maven": {"name": "io.tabforge:tabforge-data:" + version,
                      "requires": ["verified io.tabforge namespace", "Central user token in settings.xml server central", "GPG signing key and passphrase"],
                      "prepareCommand": ["mvn", "-f", "sdk/java/pom.xml", "-Pcentral-release", "verify"],
                      "command": ["mvn", "-f", "sdk/java/pom.xml", "-Pcentral-release", "deploy"]},
            "vscode": {"publisher": plugin["publisher"], "requires": ["ownership of Marketplace publisher", "VSCE_PAT", "vsce installed"],
                       "commands": [["vsce", "publish", "--pre-release", "--packagePath", str(out / ("tabforge-" + target + ".vsix"))] for target in
                                    ("darwin-arm64", "darwin-x64", "win32-x64", "win32-arm64") if (out / ("tabforge-" + target + ".vsix")).exists()]},
            "unity_cocos_godot_unreal": {"distribution": "GitHub plugin ZIPs", "requires": ["separate publisher setup and review before each engine marketplace submission"]},
        },
    }
    raw = json.dumps(plan, ensure_ascii=False, indent=2) + "\n"
    if args.plan:
        target = Path(args.plan).resolve()
        if target == out or out in target.parents:
            raise ValueError("Publish plan must be outside the sealed delivery")
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(raw, encoding="utf-8")
        print("Prepared publication plan:", target)
    else:
        print(raw, end="")


if __name__ == "__main__":
    main()
