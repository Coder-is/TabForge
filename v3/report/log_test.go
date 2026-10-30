package report

import (
	"bytes"
	"testing"
)

func TestLoggerWritesOneLinePerMessage(t *testing.T) {
	var out bytes.Buffer
	l := &Logger{out: &out}
	l.Debugln("Loading tables...")
	l.Infof("  [%s] %s", "gosrc", "a.go")
	l.Warnf("")
	l.Errorln("failed:", 1)
	if got, want := out.String(), "Loading tables...\n  [gosrc] a.go\n\nfailed: 1\n"; got != want {
		t.Fatalf("log output %q, want %q", got, want)
	}
}
