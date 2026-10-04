package helper

import (
	"fmt"
	"github.com/Coder-is/TabForge/v3/report"
	"path/filepath"
	"runtime"
	"sync"
)

type FileGetter interface {
	GetFile(filename string) (TableFile, error)
}

type FileLoader struct {
	fileByName sync.Map
	inputFile  []string

	syncLoad bool
	cacheDir string
}

type fileResult struct {
	file TableFile
	err  error
}

func (self *FileLoader) AddFile(filename string) {

	self.inputFile = append(self.inputFile, filepath.Clean(filename))
}

func (self *FileLoader) Commit() {
	self.commit(runtime.GOMAXPROCS(0), loadFileByExt)
}

// Keep at most one load per file and bound simultaneous XLSX parsing.
func (self *FileLoader) commit(workers int, load func(string, string) (TableFile, error)) {
	seen := make(map[string]bool)
	var files []string
	for _, filename := range self.inputFile {
		if !seen[filename] {
			seen[filename] = true
			files = append(files, filename)
		}
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(files) {
		workers = len(files)
	}

	jobs := make(chan string)
	var task sync.WaitGroup
	task.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer task.Done()
			for filename := range jobs {
				file, err := load(filename, self.cacheDir)
				self.fileByName.Store(filename, fileResult{file: file, err: err})
			}
		}()
	}
	for _, filename := range files {
		jobs <- filename
	}
	close(jobs)
	task.Wait()

	self.inputFile = self.inputFile[0:0]
}

func loadFileByExt(filename string, cacheDir string) (file TableFile, err error) {
	// Structured table errors must reach the caller even when loading in a worker.
	defer func() {
		if recovered := recover(); recovered != nil {
			if tableErr, ok := recovered.(*report.TableError); ok {
				file, err = nil, tableErr
			} else {
				panic(recovered)
			}
		}
	}()

	switch filepath.Ext(filename) {
	case ".xlsx", ".xls", ".xlsm":
		file = NewXlsxFile(cacheDir)
	case ".csv":
		file = NewCSVFile()
	default:
		report.ReportError("UnknownInputFileExtension", filename)
	}

	if err := file.Load(filename); err != nil {
		return nil, err
	}
	return file, nil
}

func (self *FileLoader) GetFile(filename string) (TableFile, error) {
	filename = filepath.Clean(filename)

	if self.syncLoad {
		return loadFileByExt(filename, self.cacheDir)
	}
	result, ok := self.fileByName.Load(filename)
	if !ok {
		return nil, fmt.Errorf("file not loaded: %s", filename)
	}
	loaded := result.(fileResult)
	return loaded.file, loaded.err
}

func NewFileLoader(syncLoad bool, cacheDir string) *FileLoader {
	return &FileLoader{
		syncLoad: syncLoad,
		cacheDir: cacheDir,
	}
}
