package repository

import (
	"context"

	"parser-engine/internal/model"
)

// Repository is deliberately unaware of source file formats.
type Repository interface {
	Insert(ctx context.Context, records []model.Record) error
}

// FileTransaction makes STRICT mode atomic per input file.
type FileTransaction interface {
	Repository
	Commit() error
	Rollback() error
}

type TransactionalRepository interface {
	BeginFile(ctx context.Context, fileName string) (FileTransaction, error)
	Close() error
}
