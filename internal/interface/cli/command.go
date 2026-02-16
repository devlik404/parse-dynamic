package cli

import (
	"flag"
	"fmt"
	"log"

	"parser-engine/internal/application/usecase"
)

/*
CLI Command
-----------
CLI interface untuk menjalankan parsing file.

Contoh pemakaian:
	go run cmd/app/main.go \
		-rule=RULE-001 \
		-file=sample.txt \
		-max-error=10 \
		-skip-empty=true
*/

type Command struct {
	ParseUseCase *usecase.ParseFileUseCase
}

func (c *Command) Run() {
	// =========================
	// Define flags
	// =========================
	ruleID := flag.String("rule", "", "Parsing rule ID (required)")
	filePath := flag.String("file", "", "Path to input file (required)")
	maxError := flag.Int("max-error", 0, "Maximum error allowed before stop (0 = unlimited)")
	skipEmpty := flag.Bool("skip-empty", true, "Skip empty line")

	flag.Parse()

	// =========================
	// Validate arguments
	// =========================
	if *ruleID == "" || *filePath == "" {
		fmt.Println("Usage:")
		flag.PrintDefaults()
		log.Fatal("rule and file parameters are required")
	}

	// =========================
	// Configure use case
	// =========================
	c.ParseUseCase.MaxErrorCount = *maxError
	c.ParseUseCase.SkipEmptyLine = *skipEmpty

	// =========================
	// Execute parsing
	// =========================
	if err := c.ParseUseCase.Execute(*ruleID, *filePath); err != nil {
		log.Fatalf("Parsing failed: %v", err)
	}
}
