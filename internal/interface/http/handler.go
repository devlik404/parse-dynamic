package http

import (
	"encoding/json"
	"net/http"

	"parser-engine/internal/application/usecase"
)

/*
Handler
-------
HTTP handler adapter.
*/

type Handler struct {
	ParseUseCase *usecase.ParseFileUseCase
}

func NewHandler(parseUC *usecase.ParseFileUseCase) *Handler {
	return &Handler{
		ParseUseCase: parseUC,
	}
}

// POST /parse
func (h *Handler) Parse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ParseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.RuleID == "" || req.FilePath == "" {
		http.Error(w, "rule_id and file_path are required", http.StatusBadRequest)
		return
	}

	// set runtime config
	h.ParseUseCase.MaxErrorCount = req.MaxErrorCount
	h.ParseUseCase.SkipEmptyLine = req.SkipEmptyLine

	// execute use case
	if err := h.ParseUseCase.Execute(req.RuleID, req.FilePath); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"SUCCESS"}`))
}
