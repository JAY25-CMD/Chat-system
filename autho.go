package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
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

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	if username == "" || password == "" {
		http.Redirect(w, r, "/register?error=Username+and+password+are+required", http.StatusSeeOther)
		return
	}

	if utf8.RuneCountInString(username) > 50 {
		http.Redirect(w, r, "/register?error=Username+must+be+50+characters+or+fewer", http.StatusSeeOther)
		return
	}

	if len(password) < 6 {
		http.Redirect(w, r, "/register?error=Password+must+be+at+least+6+characters", http.StatusSeeOther)
		return
	}

	// bcrypt refuses passwords longer than 72 bytes.
	if len(password) > 72 {
		http.Redirect(w, r, "/register?error=Password+is+too+long+(max+72+bytes)", http.StatusSeeOther)
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
		log.Println("register: hashing error:", err)
		http.Redirect(w, r, "/register?error=Something+went+wrong,+try+again", http.StatusSeeOther)
		return
	}

	_, err = db.Exec(
		"INSERT INTO users (username, password_hash) VALUES (?, ?)",
		username, string(passwordHash),
	)
	if err != nil {
		// Only a real duplicate username (MySQL error 1062) may show this
		// message. Every other failure used to be reported as "username
		// already taken", which hides missing tables, bad credentials,
		// truncated columns, network errors, etc.
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			http.Redirect(w, r, "/register?error=That+username+is+already+taken", http.StatusSeeOther)
			return
		}

		log.Printf("register: could not create user %q: %v", username, err)
		http.Redirect(w, r, "/register?error=Could+not+create+the+account+because+of+a+server+problem.+Please+try+again.", http.StatusSeeOther)
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

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	if username == "" || password == "" {
		http.Redirect(w, r, "/login?error=Invalid+username+or+password", http.StatusSeeOther)
		return
	}

	if locked, remaining := isLockedOut(username); locked {
		http.Redirect(w, r, "/login?error=Too+many+attempts.+Try+again+in+"+remaining.String(), http.StatusSeeOther)
		return
	}

	if _, err := db.Exec("DELETE FROM sessions WHERE expires_at < NOW()"); err != nil {
		log.Println("login: could not clean expired sessions:", err)
	}

	var userID int
	var storedHash string
	row := db.QueryRow("SELECT id, password_hash FROM users WHERE username = ?", username)
	err := row.Scan(&userID, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		recordFailedLogin(username)
		http.Redirect(w, r, "/login?error=Invalid+username+or+password", http.StatusSeeOther)
		return
	}
	if err != nil {
		// A database problem is not a wrong password. Report it as a server
		// error and log the real reason so it can be seen in Render/Layerbase
		// logs instead of confusing the user.
		log.Printf("login: could not load user %q: %v", username, err)
		http.Error(w, "Could not sign in right now because of a server problem. Please try again.", http.StatusInternalServerError)
		return
	}

	err = bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password))
	if err != nil {
		if !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			// Usually a truncated/corrupted password_hash column.
			log.Printf("login: stored password hash for user %q is unusable: %v", username, err)
		}
		recordFailedLogin(username)
		http.Redirect(w, r, "/login?error=Invalid+username+or+password", http.StatusSeeOther)
		return
	}

	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(tokenBytes); err != nil {
		log.Println("login: could not generate session token:", err)
		http.Error(w, "Could not start a session right now. Please try again.", http.StatusInternalServerError)
		return
	}
	token := hex.EncodeToString(tokenBytes)

	expiresAt := time.Now().Add(24 * time.Hour)

	if _, err = db.Exec(
		"INSERT INTO sessions (id, user_id, expires_at) VALUES (?, ?, ?)",
		token, userID, expiresAt,
	); err != nil {
		log.Printf("login: could not create session for user %d: %v", userID, err)
		http.Error(w, "Could not start a session right now. Please try again.", http.StatusInternalServerError)
		return
	}

	setAuthCookie(w, r, "session_token", token, expiresAt, true)

	csrfToken, err := generateCSRFToken()
	if err != nil {
		log.Println("login: could not generate CSRF token:", err)
		http.Error(w, "Could not start a session right now. Please try again.", http.StatusInternalServerError)
		return
	}

	setAuthCookie(w, r, "csrf_token", csrfToken, expiresAt, false)

	clearLoginAttempts(username)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

//_____________LOGOUT____________//

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !checkCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}

	cookie, err := r.Cookie("session_token")
	if err == nil {
		db.Exec("DELETE FROM sessions WHERE id = ?", cookie.Value)
	}

	clearAuthCookie(w, r, "session_token")
	clearAuthCookie(w, r, "csrf_token")

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

//_____________COOKIES____________//

func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func setAuthCookie(w http.ResponseWriter, r *http.Request, name, value string, expires time.Time, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		HttpOnly: httpOnly,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearAuthCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: name == "session_token",
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
	})
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
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	if err != nil {
		log.Println("session lookup failed:", err)
		return 0
	}

	if time.Now().After(expiresAt) {
		if _, err := db.Exec("DELETE FROM sessions WHERE id = ?", cookie.Value); err != nil {
			log.Println("could not delete expired session:", err)
		}
		return 0
	}

	return userID
}
