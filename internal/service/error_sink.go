package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"parser-engine/internal/model"
	"parser-engine/internal/securefile"
)

type ErrorSink interface {
	Write(context.Context, model.RecordError) error
	Close() error
}

type JSONErrorSink struct {
	mu      sync.Mutex
	path    string
	encoder *json.Encoder
	closer  io.Closer
}

func NewJSONErrorSink(path string) (*JSONErrorSink, error) {
	if path == "" || path == "-" {
		return &JSONErrorSink{encoder: json.NewEncoder(os.Stderr)}, nil
	}
	// Preflight the durable sink before a database connection or input write.
	// Runner discovery explicitly excludes this path (including hard-link
	// aliases), so creating it now cannot make it self-ingest as input.
	file, err := securefile.OpenAppend(path)
	if err != nil {
		return nil, fmt.Errorf("open ERROR_OUTPUT_PATH %q: %w", path, err)
	}
	return &JSONErrorSink{path: path, encoder: json.NewEncoder(file), closer: file}, nil
}

func (s *JSONErrorSink) Write(ctx context.Context, recordError model.RecordError) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	recordError.RawValue = sanitizeRawValue(recordError.RawValue, 4096, 0)
	recordError.Message = truncateString(recordError.Message, 4096)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return err
	}
	if err := s.encoder.Encode(recordError); err != nil {
		return fmt.Errorf("persist record error: %w", err)
	}
	return nil
}

func (s *JSONErrorSink) openLocked() error {
	if s.encoder != nil {
		return nil
	}
	file, err := securefile.OpenAppend(s.path)
	if err != nil {
		return fmt.Errorf("open ERROR_OUTPUT_PATH %q: %w", s.path, err)
	}
	s.encoder = json.NewEncoder(file)
	s.closer = file
	return nil
}

func (s *JSONErrorSink) Close() error {
	if s == nil || s.closer == nil {
		return nil
	}
	return s.closer.Close()
}

func sanitizeRawValue(value any, limit, depth int) any {
	if depth >= 6 {
		return "[truncated: maximum nesting depth]"
	}
	switch typed := value.(type) {
	case string:
		return truncateString(typed, limit)
	case []string:
		count := len(typed)
		if count > 100 {
			count = 100
		}
		result := make([]any, 0, count+1)
		for _, item := range typed[:count] {
			result = append(result, truncateString(item, limit))
		}
		if len(typed) > count {
			result = append(result, "[truncated: additional elements]")
		}
		return result
	case []any:
		count := len(typed)
		if count > 100 {
			count = 100
		}
		result := make([]any, 0, count+1)
		for _, item := range typed[:count] {
			result = append(result, sanitizeRawValue(item, limit, depth+1))
		}
		if len(typed) > count {
			result = append(result, "[truncated: additional elements]")
		}
		return result
	case map[string]any:
		result := make(map[string]any)
		count := 0
		for key, item := range typed {
			if count >= 100 {
				result["[truncated]"] = "additional fields"
				break
			}
			result[truncateString(key, 256)] = sanitizeRawValue(item, limit, depth+1)
			count++
		}
		return result
	default:
		return value
	}
}

func truncateString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "...[truncated]"
}

type MemoryErrorSink struct {
	mu     sync.Mutex
	Errors []model.RecordError
}

func (s *MemoryErrorSink) Write(ctx context.Context, recordError model.RecordError) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Errors = append(s.Errors, recordError)
	return nil
}

func (s *MemoryErrorSink) Close() error { return nil }
