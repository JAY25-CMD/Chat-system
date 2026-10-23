package main

import (
	"html/template"
	"net/http"
	"strings"
)

var templates = template.Must(
	template.New("").Funcs(template.FuncMap{
		"hasPrefix": strings.HasPrefix,
	}).ParseGlob("templates/*.html"),
)

func renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	// Authenticated pages must never be served from a browser/proxy cache;
	// otherwise a message POST followed by a redirect can show the old thread
	// until the user forces a refresh.
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
