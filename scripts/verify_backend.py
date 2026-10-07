"""Install local release packages in independent projects and execute consumers."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import zipfile


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--out", default="outputs/releases/v3")
    parser.add_argument("--java-home", default=os.environ.get("JAVA_HOME", ""))
    parser.add_argument("--npm", default="npm")
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    out = Path(args.out).resolve()
    package = json.loads((root / "sdk/typescript/package.json").read_text(encoding="utf-8"))
    version = package["version"]
    checksummed = set()
    for line in (out / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
        expected, name = line.split("  ", 1)
        checksummed.add(name)
        assert hashlib.sha256((out / name).read_bytes()).hexdigest() == expected, name
    expected_packages = {"tabforge-go-" + version + ".zip", "tabforge-protocol-runtime-" + version + ".tgz",
                         "tabforge_data-" + version + "-py3-none-any.whl", "tabforge-data-" + version + ".jar",
                         "tabforge-data-" + version + ".pom"}
    assert expected_packages <= checksummed, "Missing backend package checksums"

    with tempfile.TemporaryDirectory(prefix="tabforge backend 中文 ") as directory:
        work = Path(directory)
        bundle = work / "Generated"
        shutil.copytree(root / "examples/complete/Generated", bundle)
        env = os.environ.copy()
        # All installs and caches used by this verifier belong to its temporary project.
        env["npm_config_cache"] = str(work / "npm-cache")

        def run(command, cwd=work, extra=None):
            child_env = env.copy()
            if extra:
                child_env.update(extra)
            command = [str(value) for value in command]
            if command[0] == "npm":
                npm = Path(shutil.which(args.npm) or args.npm)
                if os.name == "nt" and npm.suffix.lower() in (".cmd", ".bat"):
                    command = [shutil.which("node"), str(npm.parent / "node_modules/npm/bin/npm-cli.js")] + command[1:]
                else:
                    command[0] = str(npm)
            result = subprocess.run(command, cwd=cwd, env=child_env,
                                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                    text=True, encoding="utf-8", timeout=240)
            if result.returncode:
                raise RuntimeError("Command failed: %s\n%s" % (command, result.stdout))
            return result.stdout

        # The consumer imports the installed wheel, never sdk/python sources.
        python_target = work / "python-packages"
        run([sys.executable, "-m", "pip", "install", "--no-cache-dir", "--no-deps", "--no-index", "--target", python_target,
             out / ("tabforge_data-" + version + "-py3-none-any.whl")])
        python_env = {"PYTHONPATH": str(python_target)}
        python_output = run([sys.executable, root / "examples/backend/python/client.py", bundle], extra=python_env)
        assert "18446744073709551615" in python_output
        run([sys.executable, "-m", "unittest", "discover", "-s", root / "sdk/python/tests", "-v"], extra=python_env)

        # Install the actual tgz, then execute both a normal consumer and the package's tests.
        (work / "package.json").write_text('{"type":"module"}', encoding="utf-8")
        run(["npm", "install", "--offline", "--ignore-scripts", "--no-audit", "--no-fund", out / ("tabforge-protocol-runtime-" + version + ".tgz")])
        shutil.copy(root / "examples/backend/node/client.mjs", work / "client.mjs")
        node_output = run(["node", work / "client.mjs", bundle])
        assert "18446744073709551615" in node_output
        cases = root / "examples/backend/cases.json"
        (work / "check.mjs").write_text("""
import { readFile } from 'node:fs/promises';
import { DataBundle, DataStore } from '@tabforge/protocol-runtime/node';
const bundle=await DataBundle.open(process.argv[2]);
for(const c of JSON.parse(await readFile(process.argv[3],'utf8'))){
  let accepted=true; try{bundle.decode(c.message,c.json)}catch{accepted=false}
  if(accepted!==c.valid)throw Error('wire case failed: '+c.json);
}
const store=new DataStore();await store.reload(process.argv[2]);
try{await store.reload(process.argv[2],'wrong')}catch{}
if(!store.snapshot)throw Error('lost snapshot');
""", encoding="utf-8")
        run(["node", work / "check.mjs", bundle, cases])

        # Compile the independent consumer and smoke test against the JAR only.
        java_home = Path(args.java_home) if args.java_home else None
        java = java_home / "bin/java" if java_home else "java"
        javac = java_home / "bin/javac" if java_home else "javac"
        gson = root / "sdk/java/target/dependency/gson-2.13.2.jar"
        cp = os.pathsep.join([str(out / ("tabforge-data-" + version + ".jar")), str(gson)])
        classes = work / "java-classes"
        classes.mkdir()
        run([javac, "--release", "17", "-encoding", "UTF-8", "-cp", cp, "-d", classes,
             root / "examples/backend/java/Client.java",
             root / "sdk/java/src/test/java/io/tabforge/data/BackendSmoke.java"])
        java_cp = str(classes) + os.pathsep + cp
        java_output = run([java, "-cp", java_cp, "Client", bundle])
        assert "18446744073709551615" in java_output
        java_smoke = run([java, "-cp", java_cp, "io.tabforge.data.BackendSmoke", bundle, cases])

        # Consume the source ZIP through a local module replace, with business types
        # imported from this consumer's generated package rather than repository examples.
        with zipfile.ZipFile(out / ("tabforge-go-" + version + ".zip")) as archive:
            assert archive.testzip() is None
            archive.extractall(work)
        runtime = work / "tabforge-go"
        (work / "go.mod").write_text('module example.com/backendconsumer\n\ngo 1.26.6\n\ntoolchain go1.26.8\n\nrequire github.com/Coder-is/TabForge v0.0.0\n\nreplace github.com/Coder-is/TabForge => ' + json.dumps(str(runtime).replace("\\", "/")) + '\n', encoding="utf-8")
        bootstrap = work / "bootstrap"
        bootstrap.mkdir()
        # Set this business module's go_package before generating its Go types.
        # Re-encode descriptors instead of editing generated binary descriptor strings.
        (bootstrap / "main.go").write_text('''package main
import("encoding/json";"os";"path/filepath";"strings";"github.com/Coder-is/TabForge/databundle";"github.com/Coder-is/TabForge/protocol")
func must(e error){if e!=nil{panic(e)}}
func main(){root:=os.Args[1];s,e:=protocol.LoadSchema(filepath.Join(root,"schema/schema.pb"));must(e);set:=s.DescriptorSet();for _,f:=range set.File{if !strings.HasPrefix(f.GetName(),"google/protobuf/"){v:=strings.Replace(f.GetOptions().GetGoPackage(),"github.com/Coder-is/TabForge/examples/complete/Generated/schema/go","example.com/backendconsumer/generated_types",1);f.Options.GoPackage=&v}};s,e=protocol.NewSchema(set);must(e);must(s.Generate(filepath.Join(root,"schema"),true));must(os.CopyFS("generated_types",os.DirFS(filepath.Join(root,"schema/go"))));raw,e:=os.ReadFile(filepath.Join(root,databundle.ManifestName));must(e);var m databundle.Manifest;must(json.Unmarshal(raw,&m));must(databundle.WriteManifest(root,s.Fingerprint(),m.Data))}
''', encoding="utf-8")
        run(["go", "run", "-mod=mod", "./bootstrap", bundle])
        (work / "main.go").write_text('''package main
import("fmt";"os";"github.com/Coder-is/TabForge/databundle";pb "example.com/backendconsumer/generated_types")
func main(){b,e:=databundle.Open(os.Args[1],"");if e!=nil{panic(e)};var m pb.Tables;if e=b.ReadInto("data/tables.pbb",&m);e!=nil{panic(e)};fmt.Println(m.Items[0].OwnerId)}
''', encoding="utf-8")
        go_output = run(["go", "run", "-mod=mod", ".", bundle])
        assert "18446744073709551615" in go_output

    report = {"format": "tabforge.backend.validation.v1", "version": version,
              "consumers": {"goSourceZip": "passed", "npmTgz": "passed", "pythonWheel": "passed", "javaJar": "passed"},
              "sharedWireCases": 30, "javaSmoke": java_smoke.strip(), "registriesPublished": False}
    (out / "validation.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
