package parser

import (
	"context"
	"fmt"
	"io"
	"os"
)

const (
	defaultMaxDocumentBytes = 64 * 1024 * 1024
	hardMaxDocumentBytes    = 512 * 1024 * 1024
)

func configuredMaxDocumentBytes(value int) int {
	if value <= 0 {
		return defaultMaxDocumentBytes
	}
	if value > hardMaxDocumentBytes {
		return hardMaxDocumentBytes
	}
	return value
}

// spoolDocument creates a private, bounded, seekable copy for document
// decoders whose underlying formats require random access (PDF, XLS, XLSX).
func spoolDocument(ctx context.Context, r io.Reader, maxBytes int) (file *os.File, size int64, cleanup func(), err error) {
	if r == nil {
		return nil, 0, nil, fmt.Errorf("reader is nil")
	}
	limit := configuredMaxDocumentBytes(maxBytes)
	file, err = os.CreateTemp("", "parser-document-*")
	if err != nil {
		return nil, 0, nil, fmt.Errorf("create document spool: %w", err)
	}
	cleanup = func() {
		name := file.Name()
		_ = file.Close()
		_ = os.Remove(name)
	}

	size, err = io.Copy(file, io.LimitReader(contextReader{ctx: ctx, r: r}, int64(limit)+1))
	if err != nil {
		cleanup()
		return nil, 0, nil, fmt.Errorf("copy document stream: %w", err)
	}
	if size > int64(limit) {
		cleanup()
		return nil, 0, nil, fmt.Errorf("document exceeds PARSER_MAX_DOCUMENT_BYTES")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, 0, nil, fmt.Errorf("rewind document spool: %w", err)
	}
	return file, size, cleanup, nil
}
