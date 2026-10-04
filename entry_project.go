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

	"github.com/Coder-is/TabForge/project"
)

var paramProject = flag.String("project", "", "export a tabforge.json project (file or project directory)")
var paramInit = flag.String("init", "", "create a complete portable example project without overwriting files")

//go:embed examples/complete/tabforge.json examples/complete/Tables examples/complete/Protocols examples/complete/Clients examples/complete/README.md examples/complete/Tools examples/complete/LICENSE
var completeTemplate embed.FS

func projectEntry() error {
	var conflict string
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "project", "init":
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
	path := *paramProject
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
	p, err := project.Load(path)
	if err != nil {
		return err
	}
	fmt.Printf("正在导出项目：%s\n", p.Root)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := p.Export(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("导出成功：%s（%d 个文件）\n", result.Output, len(result.Files))
	return nil
}
