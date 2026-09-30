// Package logx is a tiny leveled logger that also keeps recent lines in a
// ring buffer so they can be viewed from the web UI.
package logx

import (
	"fmt"
	"os"
	"sync"
	"time"
)

type Line struct {
	Time  int64  `json:"t"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

type Logger struct {
	mu    sync.Mutex
	buf   []Line
	next  int
	full  bool
	debug bool
}

func New(size int, debug bool) *Logger {
	return &Logger{buf: make([]Line, size), debug: debug}
}

func (l *Logger) add(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	ts := time.Now()
	fmt.Fprintf(os.Stderr, "%s %-5s %s\n", ts.Format("2006-01-02 15:04:05"), level, msg)
	l.mu.Lock()
	l.buf[l.next] = Line{Time: ts.UnixMilli(), Level: level, Msg: msg}
	l.next = (l.next + 1) % len(l.buf)
	if l.next == 0 {
		l.full = true
	}
	l.mu.Unlock()
}

func (l *Logger) Infof(format string, args ...any)  { l.add("INFO", format, args...) }
func (l *Logger) Warnf(format string, args ...any)  { l.add("WARN", format, args...) }
func (l *Logger) Errorf(format string, args ...any) { l.add("ERROR", format, args...) }
func (l *Logger) Debugf(format string, args ...any) {
	if l.debug {
		l.add("DEBUG", format, args...)
	}
}

// Lines returns buffered lines, oldest first.
func (l *Logger) Lines() []Line {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Line
	if l.full {
		out = append(out, l.buf[l.next:]...)
	}
	out = append(out, l.buf[:l.next]...)
	return out
}
