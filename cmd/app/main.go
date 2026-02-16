package main

import (
	"log"
	"net/http"

	"parser-engine/internal/application/usecase"
	"parser-engine/internal/domain/rule"
	"parser-engine/internal/infrastructure/config"
	"parser-engine/internal/infrastructure/emitter/sqlserver"
	sqlserverinfra "parser-engine/internal/infrastructure/persistence/sqlserver"
	httpiface "parser-engine/internal/interface/http"
	"parser-engine/pkg/logger"
)

func main() {
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}

	logg := logger.InitLogger(logger.Level(cfg.LogLevel))
	logg.Info("parser-engine starting")

	// =====================================================
	//  Connect DB ParamDynamic (RULE SOURCE)
	// =====================================================
	db, err := sqlserverinfra.NewConnection(sqlserverinfra.Config{
		Host:     cfg.DB.Host,
		Port:     cfg.DB.Port,
		User:     cfg.DB.User,
		Password: cfg.DB.Password,
		Database: cfg.DB.Name,
	})
	if err != nil {
		log.Fatal(err)
	}

	// =====================================================
	// Rule Repository (SQL Server ONLY)
	// =====================================================
	var ruleRepo rule.Repository
	ruleRepo = sqlserverinfra.NewRuleRepository(db)

	ruleSvc := rule.NewService()

	loadRuleUC := usecase.NewLoadRuleUseCase(ruleRepo, ruleSvc)

	recordEmitter := sqlserver.NewEmitter(db)

	parseUC := &usecase.ParseFileUseCase{
		LoadRuleUC:    loadRuleUC,
		Emitter:       recordEmitter,
		MaxErrorCount: cfg.MaxErrorCount,
		SkipEmptyLine: cfg.SkipEmptyLine,
	}

	// =====================================================
	// Run Application (HTTP MODE)
	// =====================================================
	switch cfg.Mode {
	case "http":
		handler := httpiface.NewHandler(parseUC)
		router := httpiface.NewRouter(handler)

		logg.Info("HTTP server listening on :%s", cfg.HTTPPort)
		log.Fatal(http.ListenAndServe(":"+cfg.HTTPPort, router))

	default:
		log.Fatalf("unsupported MODE: %s (only http allowed)", cfg.Mode)
	}
}
