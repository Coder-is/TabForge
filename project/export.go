package project

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Coder-is/TabForge/databundle"
	"github.com/Coder-is/TabForge/protocol"
	"github.com/Coder-is/TabForge/v3/compiler"
	"github.com/Coder-is/TabForge/v3/gen"
	"github.com/Coder-is/TabForge/v3/gen/bindata"
	"github.com/Coder-is/TabForge/v3/gen/cssrc"
	"github.com/Coder-is/TabForge/v3/gen/gosrc"
	"github.com/Coder-is/TabForge/v3/gen/javasrc"
	"github.com/Coder-is/TabForge/v3/gen/jsondata"
	"github.com/Coder-is/TabForge/v3/gen/jsontype"
	"github.com/Coder-is/TabForge/v3/gen/luasrc"
	"github.com/Coder-is/TabForge/v3/gen/pbdata"
	"github.com/Coder-is/TabForge/v3/gen/pbsrc"
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
)

type Result struct {
	Format     string     `json:"format"`
	SchemaHash string     `json:"schemaHash,omitempty"`
	Files      []string   `json:"files"`
	Output     string     `json:"-"`
	Data       []DataFile `json:"data,omitempty"`
}

// DataFile associates an exported ProtoJSON document with its root message.
type DataFile struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Export generates in a sibling staging directory, then publishes the complete
// output. Validation or generation failures leave the previous output untouched.
// Do not write handwritten files into this dedicated generated directory.
func (p *Project) Export(ctx context.Context) (*Result, error) {
	return p.generate(ctx, true)
}

// Check runs the same compiler and generators without replacing published files.
func (p *Project) Check(ctx context.Context) (*Result, error) {
	return p.generate(ctx, false)
}

func (p *Project) generate(ctx context.Context, publishOutput bool) (*Result, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(p.Root, ".tabforge-export.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("cannot lock project (another export may be running): %w", err)
	}
	defer os.Remove(lockPath)
	defer lock.Close()
	fmt.Fprintln(lock, os.Getpid())
	out, _ := relativePath(p.Root, p.Config.Output)
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(out), ".tabforge-stage-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	result := &Result{Format: "tabforge.export.v1", Output: out, Files: []string{}}
	var schema *protocol.Schema
	if p.Config.Schema != nil {
		config := p.Config.Schema
		dir, _ := relativePath(p.Root, config.Dir)
		files := append([]string(nil), config.Files...)
		if len(files) == 0 {
			err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.Type()&os.ModeSymlink != 0 {
					return fmt.Errorf("symlink Proto input: %s", path)
				}
				if !entry.IsDir() && strings.EqualFold(filepath.Ext(path), ".proto") {
					name, err := filepath.Rel(dir, path)
					if err != nil {
						return err
					}
					files = append(files, filepath.ToSlash(name))
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
		sort.Strings(files)
		imports := []string{dir}
		for _, name := range config.Imports {
			path, _ := relativePath(p.Root, name)
			imports = append(imports, path)
		}
		schema, err = protocol.CompileSchema(ctx, imports, files)
		if err != nil {
			return nil, err
		}
		if err := schema.Generate(filepath.Join(stage, "schema"), config.Go); err != nil {
			return nil, err
		}
		result.SchemaHash = schema.Fingerprint()
	}
	if p.Config.Protocol != "" {
		contract, err := protocol.LoadSourceContract(filepath.Join(p.Root, filepath.FromSlash(p.Config.Protocol)), schema)
		if err != nil {
			return nil, err
		}
		if err := contract.Generate(filepath.Join(stage, "protocol")); err != nil {
			return nil, err
		}
	}
	var entries []databundle.Entry
	for _, config := range p.Config.Tables {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := p.exportTable(config, stage); err != nil {
			return nil, &tableJobError{index: filepath.Join(p.Root, filepath.FromSlash(config.Index)), cause: err}
		}
		if config.Mapping != "" {
			data, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(config.Mapping)))
			if err != nil {
				return nil, err
			}
			var mapping pbdata.ExternalMapping
			if err := json.Unmarshal(data, &mapping); err != nil {
				return nil, err
			}
			message := strings.TrimPrefix(mapping.RootMessage, ".")
			if name := config.Outputs["protojson"]; name != "" {
				result.Data = append(result.Data, DataFile{Path: name, Message: message})
				entries = append(entries, databundle.Entry{File: databundle.File{Path: name}, Message: message, Encoding: "protojson"})
			}
			if name := config.Outputs["protobuf"]; name != "" {
				entries = append(entries, databundle.Entry{File: databundle.File{Path: name}, Message: message, Encoding: "protobuf"})
			}
			if dir := config.Outputs["protobuf_dir"]; dir != "" {
				for table := range mapping.Tables {
					entries = append(entries, databundle.Entry{File: databundle.File{Path: filepath.ToSlash(filepath.Join(dir, table+".pbb"))}, Message: message, Encoding: "protobuf"})
				}
			}
		}
	}
	if schema != nil {
		if err := databundle.WriteManifest(stage, schema.Fingerprint(), entries); err != nil {
			return nil, err
		}
		if _, err := databundle.Open(stage, schema.Fingerprint()); err != nil {
			return nil, fmt.Errorf("validate data bundle: %w", err)
		}
	}
	if err := filepath.WalkDir(stage, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			name, err := filepath.Rel(stage, path)
			if err != nil {
				return err
			}
			result.Files = append(result.Files, filepath.ToSlash(name))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	result.Files = append(result.Files, "export.json")
	sort.Strings(result.Files)
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(stage, "export.json"), append(data, '\n'), 0644); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if publishOutput {
		if err := publish(stage, out); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func publish(stage, out string) error {
	backup := out + ".tabforge-backup"
	if _, err := os.Lstat(backup); err == nil {
		return fmt.Errorf("previous backup exists at %s; recover it before exporting", backup)
	} else if !os.IsNotExist(err) {
		return err
	}
	exists := false
	if _, err := os.Lstat(out); err == nil {
		exists = true
		if err := os.Rename(out, backup); err != nil {
			return fmt.Errorf("back up previous export: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(stage, out); err != nil {
		if exists {
			if rollback := os.Rename(backup, out); rollback != nil {
				return fmt.Errorf("publish failed: %v; rollback failed: %v; previous export is at %s", err, rollback, backup)
			}
		}
		return err
	}
	if exists {
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("output published, but backup cleanup failed at %s: %w", backup, err)
		}
	}
	return nil
}

type rootedGetter struct {
	root   string
	loader helper.FileGetter
}

func (g rootedGetter) GetFile(name string) (helper.TableFile, error) {
	path, err := relativePath(g.root, name)
	if err != nil {
		return nil, err
	}
	return g.loader.GetFile(path)
}

func (p *Project) exportTable(config TableConfig, stage string) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if tableErr, ok := recovered.(*report.TableError); ok {
				err = tableErr
			} else {
				panic(recovered)
			}
		}
	}()
	index, _ := relativePath(p.Root, config.Index)
	g := model.NewGlobals()
	g.IndexFile = index
	g.PackageName = config.Package
	g.CombineStructName = config.Root
	if g.CombineStructName == "" {
		g.CombineStructName = "Table"
	}
	g.IndexGetter = helper.NewFileLoader(true, "")
	g.TableGetter = rootedGetter{filepath.Dir(index), helper.NewFileLoader(true, "")}
	g.GenBinary = config.Outputs["binary"] != "" || config.Outputs["binary_dir"] != ""
	if config.Mapping != "" {
		g.ProtoDescriptorFile = filepath.Join(stage, "schema", "schema.pb")
		g.ProtoMappingFile, _ = relativePath(p.Root, config.Mapping)
	}
	if config.Tags != "" {
		g.TagActions, err = model.ParseTagAction(config.Tags)
		if err != nil {
			return err
		}
	}
	if err := compiler.Compile(g); err != nil {
		return err
	}
	kinds := make([]string, 0, len(config.Outputs))
	for kind := range config.Outputs {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		path, err := relativePath(stage, config.Outputs[kind])
		if err != nil {
			return err
		}
		if single, ok := singleOutputs[kind]; ok {
			data, err := single(g)
			if err != nil {
				return fmt.Errorf("%s: %w", kind, err)
			}
			if err := helper.WriteFile(path, data); err != nil {
				return err
			}
		} else if err := directoryOutputs[kind](g, path); err != nil {
			return fmt.Errorf("%s: %w", kind, err)
		}
	}
	return nil
}

var singleOutputs = map[string]gen.GenSingleFile{
	"go": gosrc.Generate, "csharp": cssrc.Generate, "java": javasrc.Generate,
	"json": jsondata.Generate, "jsontype": jsontype.Generate, "lua": luasrc.Generate,
	"binary": bindata.Generate, "proto": pbsrc.Generate, "protobuf": pbdata.Generate, "protojson": pbdata.GenerateJSON,
}
var directoryOutputs = map[string]gen.GenCustom{
	"json_dir": jsondata.Output, "lua_dir": luasrc.Output,
	"binary_dir": bindata.Output, "protobuf_dir": pbdata.Output,
}

func knownOutput(kind string) bool {
	_, single := singleOutputs[kind]
	_, dir := directoryOutputs[kind]
	return single || dir
}
