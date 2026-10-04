package clientbundle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestCSharpImportedTypesAndData(t *testing.T) {
	dotnet := os.Getenv("DOTNET_BIN")
	if dotnet == "" {
		t.Skip("set DOTNET_BIN to test generated C# data; this does not run Unity Editor")
	}
	source := fixture(t)
	root := engine(t, "unity")
	r, err := Import(context.Background(), source, root, "unity", false)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := filepath.ToSlash(filepath.Join(r.Output, "Runtime", "*.cs"))
	csproj := fmt.Sprintf(`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net8.0</TargetFramework><EnableDefaultCompileItems>false</EnableDefaultCompileItems></PropertyGroup><ItemGroup><Compile Include=%s/><Compile Include="Program.cs"/><PackageReference Include="Newtonsoft.Json" Version="13.0.2"/></ItemGroup></Project>`, strconv.Quote(files))
	program := `using System; using System.IO; using TabForge.Data; using TabForge.Data.Generated;
class Program {
 static void Check(bool ok) { if(!ok) throw new Exception("Imported C# data failed"); }
 static void Main(string[] args) {
  var loader = new DataSchema(File.ReadAllText(Path.Combine(args[0],"Resources/TabForge/wire_schema.json")));
  var data = File.ReadAllText(Path.Combine(args[0],"Resources/TabForge/data/tables.json"));
  var typed = loader.Decode<M_tabforge__demo__config__Tables>(data);
  Check(typed.items.Count == 3 && typed.items[0].owner_id == "18446744073709551615" && typed.items[0].signed_total == "-9223372036854775808");
  Check(typed.items[0].reward.count == "18446744073709551615" && typed.items[0].unlock_level == 0 && typed.items[1].unlock_level == null);
  Check(typed.items[0].created_at != null && typed.items[0].bonuses != null && typed.items[0].metadata != null);
  loader.Decode<M_tabforge__demo__structures__Tree>("{\"position\":{\"x\":1,\"y\":2},\"state\":\"ACTIVE\"}");
  foreach(var invalid in new[]{"{\"unknown\":1}", "{\"items\":[{\"ownerId\":123}]}", "{\"items\":[{\"textEffect\":\"a\",\"powerEffect\":1}]}"}) {
   var rejected=false;try{loader.Decode<M_tabforge__demo__config__Tables>(invalid);}catch(DataException){rejected=true;}Check(rejected);
  }
  Console.WriteLine("C# imported data passed");
 }
}`
	if err := os.WriteFile(filepath.Join(dir, "Consumer.csproj"), []byte(csproj), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Program.cs"), []byte(program), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(dotnet, "run", "--project", filepath.Join(dir, "Consumer.csproj"), "--", r.Output)
	cmd.Env = append(os.Environ(), "DOTNET_CLI_TELEMETRY_OPTOUT=1", "DOTNET_GENERATE_ASPNET_CERTIFICATE=false", "DOTNET_CLI_HOME="+dir)
	if output, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(output), "C# imported data passed") {
		t.Fatalf("C# runtime: %v\n%s", err, output)
	}
}

func TestCocosImportedTypesAndController(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required for Cocos consumer test")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	tsc := filepath.Join(root, "sdk", "typescript", "node_modules", "typescript", "bin", "tsc")
	if _, err := os.Stat(tsc); err != nil {
		t.Skip("install sdk/typescript dev dependencies")
	}
	source := fixture(t)
	game := filepath.Join(t.TempDir(), "game 中文 $(literal)")
	if err := os.MkdirAll(filepath.Join(game, "assets"), 0755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "tabforge")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", binary, "..").CombinedOutput(); err != nil {
		t.Fatalf("build tool: %v\n%s", err, output)
	}
	controller := `const {run,argumentsFor}=require(process.argv[1]); run(process.argv[2],process.argv[3],argumentsFor(process.argv[3],'import',process.argv[4])).completion.then(r=>{if(!r.editorOutput)process.exit(1);console.log('Cocos controller passed');}).catch(e=>{console.error(e);process.exit(1);});`
	if output, err := exec.Command(node, "-e", controller, filepath.Join(root, "editors", "cocos", "runner.js"), binary, game, source).CombinedOutput(); err != nil || !strings.Contains(string(output), "Cocos controller passed") {
		t.Fatalf("Cocos controller: %v\n%s", err, output)
	}
	dest := filepath.Join(game, "assets", "resources", "tabforge")
	consumer := `import {DataSchema} from './data'; import {wireSchema} from './types'; import type {MessageTypes} from './types';
declare const require:any;
const loader=new DataSchema<MessageTypes>(wireSchema);
const text=require('fs').readFileSync(require('path').join(__dirname,'../data/tables.json'),'utf8');
const tables=loader.decode('tabforge.demo.config.Tables',text);
if(tables.items?.length!==3 || tables.items[0].ownerId!=='18446744073709551615' || tables.items[0].signedTotal!=='-9223372036854775808')throw new Error('Precision lost');
if(tables.items[0].unlockLevel!==0 || tables.items[1].unlockLevel!==undefined)throw new Error('Presence lost');
try {loader.decode('tabforge.demo.config.Tables','{"unknown":1}');throw new Error('Accepted unknown field');}catch(error){if(String(error).includes('Accepted unknown'))throw error;}
if(false){
// @ts-expect-error unknown Proto message
loader.decode('missing.Type','{}');
// @ts-expect-error uint64 remains a string
const id:number=tables.items![0].ownerId!;
}
console.log('Cocos imported types and data passed');`
	// Avoid @types/node as a runtime dependency of the delivered assets.
	consumer = "declare const __dirname:string;\n" + consumer
	if err := os.WriteFile(filepath.Join(dest, "consumer.ts"), []byte(consumer), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, tsc, "--strict", "--target", "ES2020", "--module", "commonjs", "--outDir", filepath.Join(dest, "dist"), filepath.Join(dest, "consumer.ts"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Cocos TS compile: %v\n%s", err, output)
	}
	if output, err := exec.Command(node, filepath.Join(dest, "dist", "consumer.js")).CombinedOutput(); err != nil || !strings.Contains(string(output), "Cocos imported types and data passed") {
		t.Fatalf("Cocos data runtime: %v\n%s", err, output)
	}
}

func TestGodotImportedDataAndEditorRunner(t *testing.T) {
	godot := os.Getenv("GODOT_BIN")
	if godot == "" {
		t.Skip("set GODOT_BIN to run Godot data and the actual editor process runner")
	}
	source := fixture(t)
	root := engine(t, "godot")
	if _, err := Import(context.Background(), source, root, "godot", false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plugin.cfg", "plugin.gd", "runner.gd"} {
		data, err := os.ReadFile(filepath.Join("..", "editors", "godot", name))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "addons", "tabforge", name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(t.TempDir(), "tabforge")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", binary, "..").CombinedOutput(); err != nil {
		t.Fatalf("build tool: %v\n%s", err, output)
	}
	script := `extends SceneTree
const Loader = preload("tabforge_generated/data_schema.gd")
const Schema = preload("tabforge_generated/schema.gd")
const Runner = preload("addons/tabforge/runner.gd")
var runner := Runner.new()
var frames := 0
func _initialize() -> void:
 var loader := Loader.new(Schema.WIRE_SCHEMA)
 var tables: Variant = loader.read_file("tabforge.demo.config.Tables", "res://tabforge_generated/data/tables.json")
 assert(loader.error.is_empty() and tables.items.size() == 3)
 assert(tables.items[0].ownerId == "18446744073709551615" and tables.items[0].reward.count == "18446744073709551615")
 assert(tables.items[0].unlockLevel == 0 and not tables.items[1].has("unlockLevel"))
 assert(loader.decode("tabforge.demo.config.Tables", '{"unknown":1}') == null and not loader.error.is_empty())
 var args := OS.get_cmdline_user_args()
 assert(runner.start(args[0], ProjectSettings.globalize_path("res://"), "import", args[1]))
func _process(_delta:float) -> bool:
 frames += 1
 var result: Dictionary = runner.poll()
 if not result.is_empty():
  assert(result.success, result.output)
  print("Godot imported data and editor runner passed")
  quit(0)
 elif frames > 3000:
  runner.close(); quit(1)
 return false
`
	if err := os.WriteFile(filepath.Join(root, "smoke.gd"), []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(godot, "--headless", "--path", root, "--script", "smoke.gd", "--", binary, source)
	if output, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(output), "Godot imported data and editor runner passed") {
		t.Fatalf("Godot runtime: %v\n%s", err, output)
	}
	// Enable the actual EditorPlugin in a headless editor session to check its
	// registration and native controls, beyond compiling the process runner.
	if err := os.WriteFile(filepath.Join(root, "project.godot"), []byte("config_version=5\n[editor_plugins]\nenabled=PackedStringArray(\"res://addons/tabforge/plugin.cfg\")\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(godot, "--headless", "--editor", "--path", root, "--quit-after", "30")
	if output, err := cmd.CombinedOutput(); err != nil || strings.Contains(string(output), "SCRIPT ERROR") || strings.Contains(string(output), "Parse Error") {
		t.Fatalf("Godot EditorPlugin: %v\n%s", err, output)
	}
}
