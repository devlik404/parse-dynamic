package parser

import (
	"context"
	"io"
)

const oversizedRawMarker = "[omitted: record exceeds PARSER_MAX_RECORD_BYTES]"

const (
	tooComplexRawMarker = "[omitted: record exceeds PARSER_MAX_FIELDS]"
	defaultMaxFields    = 10_000
	hardMaxFields       = 100_000
	hardMaxRecordBytes  = 8 * 1024 * 1024
)

// boundedBytes grows only up to its configured limit. Unlike append on a
// regular slice, its capacity is explicitly clipped so the runtime cannot
// over-allocate beyond the hard logical-record cap.
type boundedBytes struct {
	data     []byte
	limit    int
	exceeded bool
	disabled bool
	peakCap  int
}

func newBoundedBytes(limit int) *boundedBytes {
	limit = configuredMaxRecordBytes(limit)
	return &boundedBytes{limit: limit}
}

func (b *boundedBytes) append(values ...byte) {
	if b.exceeded || b.disabled || len(values) == 0 {
		return
	}
	if len(b.data) > b.limit-len(values) {
		b.exceeded = true
		return
	}
	required := len(b.data) + len(values)
	if required > cap(b.data) {
		capacity := cap(b.data) * 2
		if capacity < 64 {
			capacity = 64
		}
		if capacity < required {
			capacity = required
		}
		if capacity > b.limit {
			capacity = b.limit
		}
		grown := make([]byte, len(b.data), capacity)
		copy(grown, b.data)
		b.data = grown
		if capacity > b.peakCap {
			b.peakCap = capacity
		}
	}
	b.data = append(b.data, values...)
}

func (b *boundedBytes) disable() {
	b.disabled = true
}

func configuredMaxRecordBytes(value int) int {
	if value <= 0 {
		return defaultMaxRecordBytes
	}
	if value > hardMaxRecordBytes {
		return hardMaxRecordBytes
	}
	return value
}

func configuredMaxFields(value int) int {
	if value <= 0 {
		return defaultMaxFields
	}
	if value > hardMaxFields {
		return hardMaxFields
	}
	return value
}

func boundedReaderSize(maxBytes int) int {
	size := configuredMaxRecordBytes(maxBytes)
	if size > 4096 {
		size = 4096
	}
	// bufio itself enforces a minimum of 16; return it explicitly so the
	// fixed scanner overhead is visible and predictable.
	if size < 16 {
		size = 16
	}
	return size
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(buffer)
}
