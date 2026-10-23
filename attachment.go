package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const maxFileSize = 10 << 20 // 10 MB, in bytes

// change file name and store to the local disk
func generateFilename(originalName string) (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	timestamp := time.Now().UnixNano()
	ext := filepath.Ext(originalName)
	return fmt.Sprintf("%d-%s%s", timestamp, hex.EncodeToString(b), ext), nil
}

// ______FILE UPLOAD_____//
func saveUploadedFile(r *http.Request) (filename, contentType string, size int64, err error) {
	file, header, err := r.FormFile("attachment")
	if err != nil {
		// A request without a multipart body simply has no attachment. Treat
		// it like a missing file instead of failing the whole message.
		if errors.Is(err, http.ErrNotMultipart) {
			return "", "", 0, http.ErrMissingFile
		}
		return "", "", 0, err
	}
	defer file.Close()

	if header.Size > maxFileSize {
		return "", "", 0, errors.New("file too large")
	}

	// To know whether is image/video  ( display )
	contentType = header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	filename, err = generateFilename(header.Filename)
	if err != nil {
		return "", "", 0, err
	}

	//create newfile and path
	dest, err := os.Create(filepath.Join("uploads", filename))
	if err != nil {
		return "", "", 0, err
	}
	defer dest.Close()

	if _, err := io.Copy(dest, file); err != nil {
		return "", "", 0, err
	}

	return filename, contentType, header.Size, nil
}

// serveAttachmentHandler sends back a stored file, but only to someone
// who is actually a participant in the conversation it belongs to.
func serveAttachmentHandler(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	if userID == 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// which specific file
	filename := r.URL.Query().Get("file")

	var conversationID int
	var contentType string
	err := db.QueryRow(
		"SELECT conversation_id, attachment_type FROM messages WHERE attachment_path = ?",
		filename,
	).Scan(&conversationID, &contentType)
	if err != nil || !isParticipant(conversationID, userID) {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	// tell what kind of file img/pdf
	w.Header().Set("Content-Type", contentType)
	http.ServeFile(w, r, filepath.Join("uploads", filename))
}
