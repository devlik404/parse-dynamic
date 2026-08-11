package parser

import (
	"context"
	"fmt"
	"io"

	"parser-engine/internal/config"
	"parser-engine/internal/mapping"
	"parser-engine/internal/model"
	"parser-engine/internal/transformation"
	"parser-engine/internal/validation"
)

// Result contains exactly one of Record or Error.
type Result struct {
	Record *model.Record
	Error  *model.RecordError
}

// Engine composes decoding, source mapping, transformation/type conversion,
// and validation while retaining streaming backpressure.
type Engine struct {
	decoder     StreamParser
	mapper      *mapping.Engine
	transformer *transformation.Engine
	validator   *validation.Engine
}

func NewEngine(cfg config.ParserConfig) (*Engine, error) {
	decoder, err := New(cfg)
	if err != nil {
		return nil, err
	}
	transformer, err := transformation.New(cfg)
	if err != nil {
		return nil, err
	}
	return &Engine{
		decoder:     decoder,
		mapper:      mapping.New(cfg),
		transformer: transformer,
		validator:   validation.New(cfg),
	}, nil
}

func (e *Engine) Process(ctx context.Context, r io.Reader, fileName string, yield func(Result) error) error {
	if e == nil || e.decoder == nil {
		return fmt.Errorf("parser engine is nil")
	}
	if yield == nil {
		return fmt.Errorf("result callback is nil")
	}
	return e.decoder.Parse(ctx, r, fileName, func(source model.SourceRecord) error {
		record, recordErr := e.mapper.Map(source)
		if recordErr != nil {
			return yield(Result{Error: recordErr})
		}
		record, recordErr = e.transformer.Apply(record)
		if recordErr != nil {
			return yield(Result{Error: recordErr})
		}
		if recordErr = e.validator.Validate(record); recordErr != nil {
			return yield(Result{Error: recordErr})
		}
		return yield(Result{Record: &record})
	})
}
