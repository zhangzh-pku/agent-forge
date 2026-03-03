package util

import (
	"encoding/json"
	"io"
	"os"
	"time"
)

// Logger is a minimal structured JSON logger.
type Logger struct {
	fields map[string]interface{}
	out    io.Writer
}

// NewLogger creates a new logger with base fields.
func NewLogger() *Logger {
	return &Logger{fields: make(map[string]interface{}), out: os.Stderr}
}

// NewLoggerWithWriter creates a logger that writes to w.
func NewLoggerWithWriter(w io.Writer) *Logger {
	if w == nil {
		w = os.Stderr
	}
	return &Logger{fields: make(map[string]interface{}), out: w}
}

// With returns a new logger with additional fields.
func (l *Logger) With(key string, value interface{}) *Logger {
	nl := &Logger{fields: make(map[string]interface{}, len(l.fields)+1), out: l.out}
	for k, v := range l.fields {
		nl.fields[k] = v
	}
	nl.fields[key] = value
	return nl
}

// Info logs an info-level message.
func (l *Logger) Info(msg string, extra ...map[string]interface{}) {
	l.log("info", msg, extra...)
}

// Error logs an error-level message.
func (l *Logger) Error(msg string, extra ...map[string]interface{}) {
	l.log("error", msg, extra...)
}

func (l *Logger) log(level, msg string, extra ...map[string]interface{}) {
	entry := make(map[string]interface{}, len(l.fields)+4)
	for k, v := range l.fields {
		entry[k] = v
	}
	entry["level"] = level
	entry["msg"] = msg
	entry["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	for _, e := range extra {
		for k, v := range e {
			entry[k] = v
		}
	}
	data, _ := json.Marshal(entry)
	l.out.Write(data)
	l.out.Write([]byte("\n"))
}
