// Package parser contains format decoders and the format-independent streaming
// pipeline. Decoders only turn bytes into source records; they never know about
// database columns or a particular business schema.
package parser

import (
	"context"
	"io"

	"parser-engine/internal/model"
)

// YieldSource is called once for every logical source record. Returning an
// error stops parsing immediately and that error is returned to the caller.
type YieldSource func(model.SourceRecord) error

// StreamParser decodes a stream without retaining the whole input in memory.
// Recoverable record errors are yielded in SourceRecord.Error. Errors returned
// by Parse mean that the stream can no longer be decoded safely (or that the
// reader/callback failed).
type StreamParser interface {
	Parse(ctx context.Context, r io.Reader, fileName string, yield YieldSource) error
}
