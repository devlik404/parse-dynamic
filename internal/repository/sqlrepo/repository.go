package sqlrepo

import (
	"context"
	"database/sql"
	"fmt"

	"parser-engine/internal/model"
	"parser-engine/internal/repository"
)

var _ repository.TransactionalRepository = (*SQLRepository)(nil)
var _ repository.Repository = (*SQLRepository)(nil)
var _ repository.FileTransaction = (*fileTransaction)(nil)

type statementExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// SQLRepository persists canonical records and has no dependency on the input
// file format. Its immutable builder makes concurrent Insert calls safe; normal
// database/sql transaction rules still apply to fileTransaction.
type SQLRepository struct {
	db        *sql.DB
	dialect   dialect
	builder   insertBuilder
	batchSize int
}

func (r *SQLRepository) Insert(ctx context.Context, records []model.Record) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("SQL repository is not open")
	}
	return r.insert(ctx, r.db, records)
}

func (r *SQLRepository) BeginFile(ctx context.Context, _ string) (repository.FileTransaction, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("SQL repository is not open")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin file transaction: %w", err)
	}
	return &fileTransaction{repository: r, tx: tx}, nil
}

func (r *SQLRepository) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	if err := r.db.Close(); err != nil {
		return fmt.Errorf("close database: %w", err)
	}
	return nil
}

func (r *SQLRepository) insert(ctx context.Context, executor statementExecutor, records []model.Record) error {
	if len(records) == 0 {
		return nil
	}
	rowsPerStatement := r.builder.rowsPerStatement(r.batchSize)
	if rowsPerStatement < 1 {
		return fmt.Errorf("database parameter limit is lower than the configured column count")
	}

	for start := 0; start < len(records); start += rowsPerStatement {
		end := start + rowsPerStatement
		if end > len(records) {
			end = len(records)
		}
		query, args, err := r.builder.build(records[start:end])
		if err != nil {
			return fmt.Errorf("build insert batch starting at record index %d: %w", start, err)
		}
		if _, err := executor.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("execute insert batch starting at record index %d: %w", start, err)
		}
	}
	return nil
}

type fileTransaction struct {
	repository *SQLRepository
	tx         *sql.Tx
}

func (t *fileTransaction) Insert(ctx context.Context, records []model.Record) error {
	if t == nil || t.repository == nil || t.tx == nil {
		return fmt.Errorf("file transaction is not open")
	}
	return t.repository.insert(ctx, t.tx, records)
}

func (t *fileTransaction) Commit() error {
	if t == nil || t.tx == nil {
		return fmt.Errorf("file transaction is not open")
	}
	if err := t.tx.Commit(); err != nil {
		return fmt.Errorf("commit file transaction: %w", err)
	}
	return nil
}

func (t *fileTransaction) Rollback() error {
	if t == nil || t.tx == nil {
		return nil
	}
	if err := t.tx.Rollback(); err != nil && err != sql.ErrTxDone {
		return fmt.Errorf("rollback file transaction: %w", err)
	}
	return nil
}
