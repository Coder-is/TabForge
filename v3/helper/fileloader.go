package helper

import (
	"errors"
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

func (self *FileLoader) AddFile(filename string) {

	self.inputFile = append(self.inputFile, filepath.Clean(filename))
}

func (self *FileLoader) Commit() {
	self.commit(runtime.GOMAXPROCS(0), loadFileByExt)
}

// Keep at most one load per file and bound simultaneous XLSX parsing.
func (self *FileLoader) commit(workers int, load func(string, string) interface{}) {
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
				self.fileByName.Store(filename, load(filename, self.cacheDir))
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

func loadFileByExt(filename string, cacheDir string) (result interface{}) {
	// Structured table errors must reach the caller even when loading in a worker.
	defer func() {
		if recovered := recover(); recovered != nil {
			if err, ok := recovered.(*report.TableError); ok {
				result = err
			} else {
				panic(recovered)
			}
		}
	}()

	var tabFile TableFile
	switch filepath.Ext(filename) {
	case ".xlsx", ".xls", ".xlsm":

		tabFile = NewXlsxFile(cacheDir)

		err := tabFile.Load(filename)

		if err != nil {
			return err
		}

	case ".csv":
		tabFile = NewCSVFile()

		err := tabFile.Load(filename)

		if err != nil {
			return err
		}

	default:
		report.ReportError("UnknownInputFileExtension", filename)
	}

	return tabFile
}

func (self *FileLoader) GetFile(filename string) (TableFile, error) {
	filename = filepath.Clean(filename)

	if self.syncLoad {

		result := loadFileByExt(filename, self.cacheDir)
		if err, ok := result.(error); ok {
			return nil, err
		}

		return result.(TableFile), nil

	} else {
		if result, ok := self.fileByName.Load(filename); ok {

			if err, ok := result.(error); ok {
				return nil, err
			}

			return result.(TableFile), nil

		} else {
			return nil, errors.New("not found")
		}
	}

}

func NewFileLoader(syncLoad bool, cacheDir string) *FileLoader {
	return &FileLoader{
		syncLoad: syncLoad,
		cacheDir: cacheDir,
	}
}
