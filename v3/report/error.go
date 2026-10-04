package report

import (
	"fmt"
	"strings"
)

type TableError struct {
	ID string

	context []interface{}
}

// Location uses one-based source coordinates. Sheet/Cell identify workbook data.
type Location struct {
	File, Sheet, Cell string
	Line, Column      int
}

type Located interface{ SourceLocation() Location }

func (self *TableError) Locations() []Location {
	var locations []Location
	for _, item := range self.context {
		if source, ok := item.(Located); ok {
			locations = append(locations, source.SourceLocation())
		}
	}
	return locations
}

type SourceError struct {
	Location
	Cause error
}

func (e *SourceError) Error() string            { return e.Cause.Error() }
func (e *SourceError) Unwrap() error            { return e.Cause }
func (e *SourceError) SourceLocation() Location { return e.Location }

func getErrorDesc(id string) string {

	if lan, ok := ErrorByID[id]; ok {
		return lan.CHS
	}

	return ""
}

func (self *TableError) Error() string {

	var sb strings.Builder

	sb.WriteString("TableError.")
	sb.WriteString(self.ID)
	sb.WriteString(" ")
	sb.WriteString(getErrorDesc(self.ID))
	sb.WriteString(" | ")

	for index, c := range self.context {
		if index > 0 {
			sb.WriteString(" ")
		}

		sb.WriteString(fmt.Sprintf("%+v", c))
	}

	return sb.String()
}

func ReportError(id string, context ...interface{}) *TableError {

	panic(&TableError{
		ID:      id,
		context: context,
	})
}
