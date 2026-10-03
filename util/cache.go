package util

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/tealeg/xlsx"
	"io/ioutil"
	"os"
	"path/filepath"
)

type XlsxHashFile struct {
	CRC32Map map[string]uint32
	DataHash string
}

type XlsxFileCache struct {
	Name   string
	Sheets []*XlsxSheet
}

type XlsxSheet struct {
	Name  string
	Cells [][]string
}

type TableCache struct {
	z        *zip.ReadCloser
	name     string
	cacheDir string

	originFile *xlsx.File
}

func NewTableCache(name, cachedir string) *TableCache {
	return &TableCache{
		name:     name,
		cacheDir: cachedir,
	}
}

func (self *TableCache) UseCache() bool {
	return self.originFile == nil
}

func (self *TableCache) cacheFileName() string {
	return filepath.Join(self.cacheDir, self.cacheKey()+".cache")
}

func (self *TableCache) hashFileName() string {
	return filepath.Join(self.cacheDir, self.cacheKey()+".hash")
}

func (self *TableCache) cacheKey() string {
	name, err := filepath.Abs(self.name)
	if err != nil {
		name = filepath.Clean(self.name)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
}

func (self *TableCache) Open() error {
	if err := self.Close(); err != nil {
		return err
	}
	self.originFile = nil

	z, err := zip.OpenReader(self.name)
	if err != nil {
		return err
	}

	self.z = z

	return nil
}

func (self *TableCache) Close() error {
	if self.z == nil {
		return nil
	}
	err := self.z.Close()
	self.z = nil
	return err
}

func readJsonFile(filename string, m interface{}) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	return json.NewDecoder(f).Decode(m)
}

func writeJsonFile(filename string, m interface{}) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeCacheFile(filename, data)
}

// Publish a complete file only after its contents have been written and closed.
func writeCacheFile(filename string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		return err
	}
	f, err := ioutil.TempFile(filepath.Dir(filename), ".tabtoy-cache-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filename)
}

func (self *TableCache) readCache() (xf *xlsx.File, err error) {

	var hashFile XlsxHashFile
	err = readJsonFile(self.hashFileName(), &hashFile)
	if err != nil {
		return nil, nil
	}

	if len(self.z.File) != len(hashFile.CRC32Map) {
		return nil, nil
	}

	for _, f := range self.z.File {
		if crc, ok := hashFile.CRC32Map[f.Name]; !ok || crc != f.CRC32 {
			return nil, nil
		}
	}

	var cacheFile XlsxFileCache
	data, err := ioutil.ReadFile(self.cacheFileName())
	if err != nil {
		return nil, nil
	}
	// Binding the metadata to the data also detects interrupted/concurrent saves.
	if hashFile.DataHash != fmt.Sprintf("%x", sha256.Sum256(data)) {
		return nil, nil
	}
	if err := json.Unmarshal(data, &cacheFile); err != nil || len(cacheFile.Sheets) == 0 || cacheFile.Name != self.name {
		return nil, nil
	}

	xf = xlsx.NewFile()

	for _, s := range cacheFile.Sheets {
		if s == nil {
			return nil, nil
		}
		sheet, err := xf.AddSheet(s.Name)
		if err != nil {
			return nil, err
		}
		for _, srcRow := range s.Cells {
			tgtRow := sheet.AddRow()

			for _, c := range srcRow {
				cell := tgtRow.AddCell()
				cell.Value = c
			}

		}
	}

	return
}

func (self *TableCache) Load() (xf *xlsx.File, err error) {
	if self.z == nil {
		return nil, fmt.Errorf("cache source is not open: %s", self.name)
	}

	cfile, err := self.readCache()

	// cache未击中, 从原文件读取
	if err != nil || cfile == nil {
		// Keep ownership of the ZIP handle; ReadZipWithRowLimit closes it itself.
		xf, err = xlsx.ReadZipReaderWithRowLimit(&self.z.Reader, xlsx.NoRowLimit)
		self.originFile = xf
		return
	}

	self.originFile = nil
	return cfile, nil
}

func (self *TableCache) Save() error {
	if self.z == nil {
		return fmt.Errorf("cache source is not open: %s", self.name)
	}
	if self.originFile == nil {
		return nil
	}

	var hashFile XlsxHashFile
	hashFile.CRC32Map = make(map[string]uint32)

	for _, f := range self.z.File {
		hashFile.CRC32Map[f.Name] = f.CRC32
	}

	var newFile XlsxFileCache
	newFile.Name = self.name

	for _, sheet := range self.originFile.Sheets {

		var newSheet XlsxSheet
		newSheet.Name = sheet.Name

		newSheet.Cells = make([][]string, 0, len(sheet.Rows))

		for _, row := range sheet.Rows {

			var rowData = make([]string, 0, len(row.Cells))
			for _, c := range row.Cells {
				rowData = append(rowData, c.Value)
			}

			newSheet.Cells = append(newSheet.Cells, rowData)
		}

		newFile.Sheets = append(newFile.Sheets, &newSheet)
	}

	data, err := json.Marshal(&newFile)
	if err != nil {
		return err
	}
	hashFile.DataHash = fmt.Sprintf("%x", sha256.Sum256(data))
	if err := writeCacheFile(self.cacheFileName(), data); err != nil {
		return err
	}
	return writeJsonFile(self.hashFileName(), &hashFile)
}
