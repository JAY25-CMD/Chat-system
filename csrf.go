package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

func generateCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func csrfCookieValue(r *http.Request) string {
	cookie, err := r.Cookie("csrf_token")
	if err != nil {
		return ""
	}
	return cookie.Value
}

func checkCSRF(r *http.Request) bool {
	cookieToken := csrfCookieValue(r)
	formToken := r.FormValue("csrf_token")
	return cookieToken != "" && cookieToken == formToken
}
