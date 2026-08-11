package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"parser-engine/internal/config"
	"parser-engine/internal/repository"
	"parser-engine/internal/repository/sqlrepo"
	"parser-engine/internal/service"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Stdout); err != nil {
		log.Printf("parser job failed: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer) error {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		return fmt.Errorf("configuration invalid: %w", err)
	}
	if _, err := service.DiscoverInputs(cfg); err != nil {
		return fmt.Errorf("input preflight failed: %w", err)
	}

	errorSink, err := service.NewJSONErrorSink(cfg.Error.OutputPath)
	if err != nil {
		return err
	}
	repo, err := sqlrepo.OpenContext(ctx, cfg.DB)
	if err != nil {
		return errors.Join(err, closeErrorSink(errorSink))
	}
	return runConfigured(ctx, output, cfg, repo, errorSink)
}

func runConfigured(ctx context.Context, output io.Writer, cfg config.JobConfig, repo repository.TransactionalRepository, errorSink service.ErrorSink) error {
	runner, err := service.NewRunner(cfg, repo, errorSink)
	if err != nil {
		return errors.Join(err, closeRuntime(repo, errorSink))
	}
	summary, runErr := runner.Run(ctx)
	closeErr := closeRuntime(repo, errorSink)
	if closeErr != nil && runErr == nil {
		// Records may already be committed at this point. Make that distinction
		// explicit so an operator does not blindly retry a misleading SUCCESS.
		summary.Status = "COMPLETED_WITH_CLOSE_ERROR"
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(summary); err != nil {
		return errors.Join(runErr, closeErr, fmt.Errorf("write job summary: %w", err))
	}
	return errors.Join(runErr, closeErr)
}

func closeRuntime(repo repository.TransactionalRepository, errorSink service.ErrorSink) error {
	var result error
	if errorSink != nil {
		result = errors.Join(result, closeErrorSink(errorSink))
	}
	if repo != nil {
		if err := repo.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("close repository: %w", err))
		}
	}
	return result
}

func closeErrorSink(errorSink service.ErrorSink) error {
	if errorSink == nil {
		return nil
	}
	if err := errorSink.Close(); err != nil {
		return fmt.Errorf("close error sink: %w", err)
	}
	return nil
}
