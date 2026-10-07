package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/Coder-is/TabForge/clientbundle"
	"github.com/Coder-is/TabForge/project"
)

var paramProject = flag.String("project", "", "export a tabforge.json project (file or project directory)")
var paramInit = flag.String("init", "", "create a complete portable example project without overwriting files")
var paramCheck = flag.Bool("check", false, "validate a project or editor import without publishing generated assets")
var paramReport = flag.Bool("report", false, "write .tabforge-report.json for editor diagnostics")
var paramImport = flag.String("import", "", "import an existing Generated bundle without source files")
var paramEditor = flag.String("editor", "", "editor client: unity, cocos, godot or unreal")
var paramEditorProject = flag.String("editor_project", "", "engine project directory receiving generated assets")

//go:embed examples/complete/tabforge.json examples/complete/Tables examples/complete/Protocols examples/complete/Clients examples/complete/README.md examples/complete/Tools examples/complete/LICENSE
var completeTemplate embed.FS

func projectEntry() (runErr error) {
	path := *paramProject
	if *paramInit != "" {
		path = *paramInit
	}
	reportRoot := *paramEditorProject
	if reportRoot != "" {
		reportRoot, _ = filepath.Abs(reportRoot)
	}
	if reportRoot == "" && path != "" {
		reportRoot, _ = filepath.Abs(path)
		if stat, err := os.Stat(reportRoot); err == nil && !stat.IsDir() {
			reportRoot = filepath.Dir(reportRoot)
		}
	}
	var p *project.Project
	report := project.RunReport{Action: "export"}
	if *paramCheck {
		report.Action = "check"
	}
	if *paramImport != "" {
		report.Action = "import"
	}
	defer func() {
		if !*paramReport || reportRoot == "" {
			return
		}
		report.Success = runErr == nil
		report.Project = reportRoot
		if p == nil {
			p = &project.Project{Root: reportRoot, ConfigPath: filepath.Join(reportRoot, "tabforge.json")}
		}
		report.Diagnostics = p.Diagnose(runErr)
		if err := project.WriteReport(reportRoot, report); err != nil {
			if runErr == nil {
				runErr = fmt.Errorf("operation completed, but report could not be written: %w", err)
			} else {
				runErr = fmt.Errorf("%w; write report: %v", runErr, err)
			}
		}
	}()
	var conflict string
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "project", "init", "check", "report", "import", "editor", "editor_project":
		default:
			conflict = f.Name
		}
	})
	if conflict != "" {
		return fmt.Errorf("project export uses tabforge.json; remove -%s", conflict)
	}
	if *paramProject != "" && *paramInit != "" {
		return fmt.Errorf("-project and -init cannot be combined")
	}
	if (*paramEditor == "") != (*paramEditorProject == "") {
		return fmt.Errorf("-editor and -editor_project must be specified together")
	}
	if *paramEditor != "" && *paramEditor != "unity" && *paramEditor != "cocos" && *paramEditor != "godot" && *paramEditor != "unreal" {
		return fmt.Errorf("unknown editor %q", *paramEditor)
	}
	if *paramImport != "" && (path != "" || *paramEditor == "") {
		return fmt.Errorf("-import requires an editor target and cannot be combined with -project or -init")
	}
	if *paramCheck && *paramInit != "" {
		return fmt.Errorf("-check cannot initialize a project")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *paramImport != "" {
		result, err := clientbundle.Import(ctx, *paramImport, *paramEditorProject, *paramEditor, *paramCheck)
		if err != nil {
			return err
		}
		report.EditorOutput = result.Output
		fmt.Printf("协议包%s成功：%s（%d 个文件）\n", map[bool]string{true: "校验", false: "导入"}[*paramCheck], result.Output, len(result.Files))
		return nil
	}
	if *paramInit != "" {
		template, err := fs.Sub(completeTemplate, "examples/complete")
		if err != nil {
			return err
		}
		if err := project.Init(*paramInit, template); err != nil {
			return err
		}
		toolName := "tabforge"
		if runtime.GOOS == "windows" {
			toolName += ".exe"
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(executable)
		if err != nil {
			return err
		}
		toolPath := filepath.Join(*paramInit, "Tools", "TabForge", toolName)
		f, err := os.OpenFile(toolPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0755)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		path = *paramInit
	}
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		path, err = project.Find(cwd)
		if err != nil {
			// Double-clicked portable tools discover their enclosing project.
			executable, exeErr := os.Executable()
			if exeErr != nil {
				return err
			}
			path, err = project.Find(filepath.Dir(executable))
			if err != nil {
				return err
			}
		}
	}
	var err error
	p, err = project.Load(path)
	if err != nil {
		return err
	}
	if reportRoot == "" {
		reportRoot = p.Root
	}
	fmt.Printf("正在%s项目：%s\n", map[bool]string{true: "校验", false: "导出"}[*paramCheck], p.Root)
	var result *project.Result
	if *paramCheck {
		result, err = p.Check(ctx)
	} else {
		result, err = p.Export(ctx)
	}
	if err != nil {
		return err
	}
	report.Result, report.Output = result, result.Output
	if *paramEditor != "" && !*paramCheck {
		imported, err := clientbundle.Import(ctx, result.Output, *paramEditorProject, *paramEditor, false)
		if err != nil {
			return fmt.Errorf("project exported; editor import failed: %w", err)
		}
		report.EditorOutput = imported.Output
	}
	fmt.Printf("%s成功：%s（%d 个文件）\n", map[bool]string{true: "校验", false: "导出"}[*paramCheck], result.Output, len(result.Files))
	return nil
}
