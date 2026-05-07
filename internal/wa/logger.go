package wa

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	waLog "go.mau.fi/whatsmeow/util/log"
)

type whatsmeowLogger struct {
	module string
	min    int
	w      io.Writer
	mu     *sync.Mutex
}

var _ waLog.Logger = (*whatsmeowLogger)(nil)

var whatsmeowLogLevels = map[string]int{
	"":      -1,
	"DEBUG": 0,
	"INFO":  1,
	"WARN":  2,
	"ERROR": 3,
}

func newWhatsmeowLogger(module, minLevel string, w io.Writer) *whatsmeowLogger {
	if w == nil {
		w = io.Discard
	}
	min, ok := whatsmeowLogLevels[strings.ToUpper(minLevel)]
	if !ok {
		min = whatsmeowLogLevels["ERROR"]
	}
	return &whatsmeowLogger{
		module: module,
		min:    min,
		w:      w,
		mu:     &sync.Mutex{},
	}
}

func (l *whatsmeowLogger) Errorf(msg string, args ...interface{}) { l.outputf("ERROR", msg, args...) }
func (l *whatsmeowLogger) Warnf(msg string, args ...interface{})  { l.outputf("WARN", msg, args...) }
func (l *whatsmeowLogger) Infof(msg string, args ...interface{})  { l.outputf("INFO", msg, args...) }
func (l *whatsmeowLogger) Debugf(msg string, args ...interface{}) { l.outputf("DEBUG", msg, args...) }

func (l *whatsmeowLogger) Sub(module string) waLog.Logger {
	return &whatsmeowLogger{
		module: fmt.Sprintf("%s/%s", l.module, module),
		min:    l.min,
		w:      l.w,
		mu:     l.mu,
	}
}

func (l *whatsmeowLogger) outputf(level, msg string, args ...interface{}) {
	levelValue, ok := whatsmeowLogLevels[level]
	if !ok || levelValue < l.min {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "%s [%s %s] %s\n", time.Now().Format("15:04:05.000"), l.module, level, fmt.Sprintf(msg, args...))
}
