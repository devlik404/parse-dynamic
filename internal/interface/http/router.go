package http

import "net/http"

/*
NewRouter
---------
Router HTTP sederhana.
Framework-less supaya ringan & jelas.
*/

func NewRouter(handler *Handler) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/parse", handler.Parse)

	return mux
}
