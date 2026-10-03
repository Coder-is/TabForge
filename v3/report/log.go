package report

import (
	"github.com/davyxu/golog"
)

var Log = golog.New("TabForge")

func init() {
	Log.SetParts()
}
