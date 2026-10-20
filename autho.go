package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type LoginPageData struct {
	Error   string
	Success string
}

//_____________REGISTER____________//

func registerHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if currentUser(r) != 0 {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}
		errorMsg := r.URL.Query().Get("error")

		renderTemplate(w, "register.html", errorMsg)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		http.Redirect(w, r, "/register?error=Username+and+password+are+required", http.StatusSeeOther)
		return
	}

	if len(password) < 6 {
		http.Redirect(w, r, "/register?error=Password+must+be+at+least+6+characters", http.StatusSeeOther)
		return
	}

	hasUppercase, hasLowercase, hasNumber := false, false, false
	for _, char := range password {
		hasUppercase = hasUppercase || char >= 'A' && char <= 'Z'
		hasLowercase = hasLowercase || char >= 'a' && char <= 'z'
		hasNumber = hasNumber || char >= '0' && char <= '9'
	}
	if !hasUppercase || !hasLowercase || !hasNumber {
		http.Redirect(w, r, "/register?error=Password+must+contain+an+uppercase+letter,+a+lowercase+letter,+and+a+number", http.StatusSeeOther)
		return
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		fmt.Println("hashing error:", err)
		http.Redirect(w, r, "/register?error=Something+went+wrong,+try+again", http.StatusSeeOther)
		return
	}

	_, err = db.Exec(
		"INSERT INTO users (username, password_hash) VALUES (?, ?)",
		username, string(passwordHash),
	)
	if err != nil {
		fmt.Println("insert error:", err)
		http.Redirect(w, r, "/register?error=That+username+is+already+taken", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/login?success=Account+created!+Please+log+in.", http.StatusSeeOther)
}

//_____________LOGIN____________//

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if currentUser(r) != 0 {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}
		data := LoginPageData{
			Error:   r.URL.Query().Get("error"),
			Success: r.URL.Query().Get("success"),
		}
		renderTemplate(w, "login.html", data)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	if locked, remaining := isLockedOut(username); locked {
		http.Redirect(w, r, "/login?error=Too+many+attempts.+Try+again+in+"+remaining.String(), http.StatusSeeOther)
		return
	}

	db.Exec("DELETE FROM sessions WHERE expires_at < NOW()")

	var userID int
	var storedHash string
	row := db.QueryRow("SELECT id, password_hash FROM users WHERE username = ?", username)
	err := row.Scan(&userID, &storedHash)
	if err != nil {
		recordFailedLogin(username)
		http.Redirect(w, r, "/login?error=Invalid+username+or+password", http.StatusSeeOther)
		return
	}

	err = bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password))
	if err != nil {
		recordFailedLogin(username)
		http.Redirect(w, r, "/login?error=Invalid+username+or+password", http.StatusSeeOther)
		return
	}

	tokenBytes := make([]byte, 32)
	_, err = rand.Read(tokenBytes)
	if err != nil {
		http.Error(w, "could not start session", http.StatusInternalServerError)
		return
	}
	token := hex.EncodeToString(tokenBytes)

	expiresAt := time.Now().Add(24 * time.Hour)

	_, err = db.Exec(
		"INSERT INTO sessions (id, user_id, expires_at) VALUES (?, ?, ?)",
		token, userID, expiresAt,
	)
	if err != nil {
		http.Error(w, "could not start session", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    token,
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   false, // TRUE IF IT RUN ON HTTPS//
		Path:     "/",
	})

	csrfToken, err := generateCSRFToken()
	if err != nil {
		http.Error(w, "could not start session", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:    "csrf_token",
		Value:   csrfToken,
		Expires: expiresAt,
		Path:    "/",
	})

	clearLoginAttempts(username)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

//_____________LOGOUT____________//

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}

	cookie, err := r.Cookie("session_token")
	if err == nil {
		db.Exec("DELETE FROM sessions WHERE id = ?", cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    "",
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Path:     "/",
	})

	http.SetCookie(w, &http.Cookie{
		Name:    "csrf_token",
		Value:   "",
		Expires: time.Unix(0, 0),
		Path:    "/",
	})

	http.Redirect(w, r, "/login", http.StatusSeeOther)

	if !checkCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
}

//_____________CURRENTUSER____________//

func currentUser(r *http.Request) int {
	cookie, err := r.Cookie("session_token")
	if err != nil {
		return 0
	}

	var userID int
	var expiresAt time.Time
	row := db.QueryRow(
		"SELECT user_id, expires_at FROM sessions WHERE id = ?",
		cookie.Value,
	)
	err = row.Scan(&userID, &expiresAt)
	if err != nil {
		return 0
	}

	if time.Now().After(expiresAt) {
		return 0
	}

	return userID
}
