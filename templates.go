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
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
