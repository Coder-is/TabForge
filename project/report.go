package project

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Coder-is/TabForge/v3/report"
	"github.com/bufbuild/protocompile/reporter"
)

const ReportName = ".tabforge-report.json"

// Diagnostic carries one-based text coordinates or workbook sheet/cell names.
type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Sheet   string `json:"sheet,omitempty"`
	Cell    string `json:"cell,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Hint    string `json:"hint,omitempty"`
}

type RunReport struct {
	Format       string       `json:"format"`
	Action       string       `json:"action"`
	Success      bool         `json:"success"`
	Project      string       `json:"project"`
	Output       string       `json:"output,omitempty"`
	EditorOutput string       `json:"editorOutput,omitempty"`
	Result       *Result      `json:"result,omitempty"`
	Diagnostics  []Diagnostic `json:"diagnostics"`
}

type tableJobError struct {
	index string
	cause error
}

func (e *tableJobError) Error() string { return fmt.Sprintf("%s: %v", e.index, e.cause) }
func (e *tableJobError) Unwrap() error { return e.cause }

// Diagnose keeps error identity and positions without parsing display strings.
func (p *Project) Diagnose(err error) []Diagnostic {
	if err == nil {
		return []Diagnostic{}
	}
	d := Diagnostic{Code: "export_failed", Message: err.Error(), Path: p.ConfigPath, Hint: "修正源文件后重新校验；导出失败会保留上一版生成目录。"}
	var job *tableJobError
	base := p.Root
	if errors.As(err, &job) {
		base = filepath.Dir(job.index)
		d.Path = job.index
	}
	var discovery *DiscoveryError
	if errors.As(err, &discovery) {
		d.Code, d.Path = "discovery_input", discovery.Path
		d.Hint = "按约定目录和文件名放置输入；新增业务类型请由程序维护发现规则。"
		return []Diagnostic{d}
	}
	var source reporter.ErrorWithPos
	if errors.As(err, &source) {
		pos := source.GetPosition()
		d.Code, d.Line, d.Column = "proto_compile", pos.Line, pos.Col
		dirs := []string{}
		if p.Config.Schema != nil {
			dirs = append(dirs, p.Config.Schema.Dir)
			dirs = append(dirs, p.Config.Schema.Imports...)
		}
		for _, dir := range dirs {
			file, pathErr := relativePath(filepath.Join(p.Root, filepath.FromSlash(dir)), pos.Filename)
			if pathErr == nil {
				if _, statErr := os.Stat(file); statErr == nil {
					d.Path = file
					break
				}
			}
		}
		return []Diagnostic{d}
	}
	var table *report.TableError
	if errors.As(err, &table) {
		d.Code = table.ID
		if table.ID == "DuplicateValueInMakingIndex" {
			d.Hint = "索引值必须唯一；检查同一表和参与合并的其他文件是否存在相同 ID。"
		}
		if locations := table.Locations(); len(locations) > 0 {
			return locatedDiagnostics(d, base, locations)
		}
	}
	var located report.Located
	if errors.As(err, &located) {
		d.Code = "invalid_data"
		return locatedDiagnostics(d, base, []report.Location{located.SourceLocation()})
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && pathErr.Path != "" {
		d.Path = pathErr.Path
		d.Hint = "检查文件是否存在、是否可读取，以及项目中的文件名是否一致。"
	}
	return []Diagnostic{d}
}

func locatedDiagnostics(d Diagnostic, base string, locations []report.Location) []Diagnostic {
	var result []Diagnostic
	for _, l := range locations {
		item := d
		if l.File != "" {
			item.Path = l.File
			if !filepath.IsAbs(item.Path) {
				item.Path = filepath.Join(base, filepath.FromSlash(item.Path))
			}
		}
		item.Sheet, item.Cell, item.Line, item.Column = l.Sheet, l.Cell, l.Line, l.Column
		if strings.EqualFold(filepath.Ext(item.Path), ".csv") && item.Line > 0 && item.Column > 0 {
			// Table rows/columns identify CSV records/fields, not physical text.
			if file, err := os.Open(item.Path); err == nil {
				reader := csv.NewReader(file)
				reader.FieldsPerRecord = -1
				for row := 1; row <= l.Line; row++ {
					record, err := reader.Read()
					if err != nil {
						break
					}
					if row == l.Line && l.Column <= len(record) {
						item.Line, item.Column = reader.FieldPos(l.Column - 1)
					}
				}
				file.Close()
			}
		}
		result = append(result, item)
	}
	return result
}

// WriteReport replaces a fixed sidecar outside generated assets and inputs.
func WriteReport(root string, r RunReport) error {
	r.Format = "tabforge.report.v1"
	if r.Diagnostics == nil {
		r.Diagnostics = []Diagnostic{}
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(root, ReportName)
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("report cannot replace a symlink: %s", path)
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}
