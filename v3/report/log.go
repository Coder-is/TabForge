package report

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// Logger writes plain progress lines to stdout. It replaces the unlicensed
// github.com/davyxu/golog dependency with the same output: the message text,
// terminated by exactly one newline, without prefixes or colors.
type Logger struct {
	mu  sync.Mutex
	out io.Writer
}

var Log = &Logger{out: os.Stdout}

func (l *Logger) write(text string) {
	if len(text) == 0 || text[len(text)-1] != '\n' {
		text += "\n"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	io.WriteString(l.out, text)
}

func (l *Logger) Debugf(format string, v ...interface{}) { l.write(fmt.Sprintf(format, v...)) }
func (l *Logger) Infof(format string, v ...interface{})  { l.write(fmt.Sprintf(format, v...)) }
func (l *Logger) Warnf(format string, v ...interface{})  { l.write(fmt.Sprintf(format, v...)) }
func (l *Logger) Errorf(format string, v ...interface{}) { l.write(fmt.Sprintf(format, v...)) }
func (l *Logger) Debugln(v ...interface{})               { l.write(fmt.Sprintln(v...)) }
func (l *Logger) Infoln(v ...interface{})                { l.write(fmt.Sprintln(v...)) }
func (l *Logger) Warnln(v ...interface{})                { l.write(fmt.Sprintln(v...)) }
func (l *Logger) Errorln(v ...interface{})               { l.write(fmt.Sprintln(v...)) }
