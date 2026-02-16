package logger

import (
	"log"
	"os"
	"sync"
)

/*
Logger
------
Simple structured logger wrapper.

Level:
- DEBUG
- INFO
- WARN
- ERROR
*/

type Level string

const (
	Debug Level = "DEBUG"
	Info  Level = "INFO"
	Warn  Level = "WARN"
	Error Level = "ERROR"
)

type Logger struct {
	mu     sync.Mutex
	level  Level
	logger *log.Logger
}

var (
	instance *Logger
	once     sync.Once
)

// InitLogger
// ----------
// Inisialisasi logger singleton.
// level: DEBUG | INFO | WARN | ERROR
func InitLogger(level Level) *Logger {
	once.Do(func() {
		instance = &Logger{
			level:  level,
			logger: log.New(os.Stdout, "", log.LstdFlags),
		}
	})
	return instance
}

// GetLogger
// ---------
// Ambil instance logger (pastikan InitLogger dipanggil dulu)
func GetLogger() *Logger {
	if instance == nil {
		// default fallback
		return InitLogger(Info)
	}
	return instance
}

// ========================
// Logging methods
// ========================

func (l *Logger) Debug(msg string, args ...interface{}) {
	if l.allow(Debug) {
		l.print(Debug, msg, args...)
	}
}

func (l *Logger) Info(msg string, args ...interface{}) {
	if l.allow(Info) {
		l.print(Info, msg, args...)
	}
}

func (l *Logger) Warn(msg string, args ...interface{}) {
	if l.allow(Warn) {
		l.print(Warn, msg, args...)
	}
}

func (l *Logger) Error(msg string, args ...interface{}) {
	if l.allow(Error) {
		l.print(Error, msg, args...)
	}
}

// ========================
// Internal helpers
// ========================

func (l *Logger) allow(level Level) bool {
	order := map[Level]int{
		Debug: 1,
		Info:  2,
		Warn:  3,
		Error: 4,
	}
	return order[level] >= order[l.level]
}

func (l *Logger) print(level Level, msg string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	prefix := "[" + string(level) + "] "
	l.logger.Printf(prefix+msg, args...)
}
