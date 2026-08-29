package preview

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

type requestLoggerContextKey struct{}

var fallbackRequestSequence atomic.Uint64

func withRequestLogger(ctx context.Context, logger *slog.Logger) context.Context {
	if logger == nil {
		return ctx
	}
	return context.WithValue(ctx, requestLoggerContextKey{}, logger)
}

func requestLogger(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if ctx != nil {
		if logger, ok := ctx.Value(requestLoggerContextKey{}).(*slog.Logger); ok && logger != nil {
			return logger
		}
	}
	if fallback != nil {
		return fallback
	}
	return slog.New(discardSlogHandler{})
}

func newRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	// A cryptographic failure is exceptional. Time plus an atomic process-local
	// sequence remains unique enough for correlation without trusting input.
	return fmt.Sprintf("%016x%016x", uint64(time.Now().UnixNano()), fallbackRequestSequence.Add(1))
}

type discardSlogHandler struct{}

func (discardSlogHandler) Enabled(context.Context, slog.Level) bool   { return false }
func (discardSlogHandler) Handle(context.Context, slog.Record) error  { return nil }
func (handler discardSlogHandler) WithAttrs([]slog.Attr) slog.Handler { return handler }
func (handler discardSlogHandler) WithGroup(string) slog.Handler      { return handler }
