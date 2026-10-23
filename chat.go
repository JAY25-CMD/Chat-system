package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxMessageLength = 500

type ChatMessage struct {
	ID             int
	Username       string
	Content        string
	CreatedAt      time.Time
	Mine           bool
	Delivered      bool
	Deleted        bool
	Read           bool
	AttachmentPath string
	AttachmentName string
	AttachmentType string
}
type ConversationSummary struct {
	ID          int
	Type        string
	ChatWith    string
	LastMessage sql.NullString
	LastSender  sql.NullString
	LastTime    sql.NullTime
	Unread      int
}

type GroupMember struct {
	ID       int
	Username string
}

type MessagesFragmentData struct {
	Messages         []ChatMessage
	ConversationType string
	CSRFToken        string
}

// ______ user belongs to a conversation_____//
func isParticipant(conversationID, userID int) bool {
	var one int
	err := db.QueryRow(
		"SELECT 1 FROM participants WHERE conversation_id = ? AND user_id = ?",
		conversationID, userID,
	).Scan(&one)
	return err == nil
}

// _____find or create chat______//
func findOrCreateDirect(userA, userB int) (int, error) {
	if userA == userB {
		return 0, errors.New("cannot start a chat with yourself")
	}

	var conversationID int
	err := db.QueryRow(`
		SELECT c.id
		FROM conversations c
		JOIN participants p1 ON p1.conversation_id = c.id AND p1.user_id = ?
		JOIN participants p2 ON p2.conversation_id = c.id AND p2.user_id = ?
		WHERE c.type = 'direct'
		LIMIT 1`, userA, userB).Scan(&conversationID)
	if err == nil {
		return conversationID, nil
	}
	//handle error of of db
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	// No chat yet create the conversation(bubble room)
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	result, err := tx.Exec("INSERT INTO conversations (type) VALUES ('direct')")
	if err != nil {
		return 0, err
	}
	newID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	_, err = tx.Exec(
		"INSERT INTO participants (conversation_id, user_id) VALUES (?, ?), (?, ?)",
		newID, userA, newID, userB,
	)
	if err != nil {
		return 0, err
	}
	// handle any error occur at a lastsec in bubble room
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(newID), nil
}

// otherParticipantName returns the username of the other person in a
// one-to-one chat,
func otherParticipantName(conversationID, userID int) string {
	var name string
	db.QueryRow(`
		SELECT users.username
		FROM participants
		JOIN users ON users.id = participants.user_id
		WHERE participants.conversation_id = ? AND participants.user_id != ?
		LIMIT 1`, conversationID, userID).Scan(&name)
	return name
}

// returns every message in a conversation, oldest first.
func getConversationMessages(conversationID, viewerID int) ([]ChatMessage, error) {
	// counts as read or delivered only once every other member has reached it.
	var othersMinRead, othersMinDelivered int
	db.QueryRow(`
		SELECT COALESCE(MIN(last_read_message_id), 0),
		       COALESCE(MIN(last_delivered_message_id), 0)
		FROM participants
		WHERE conversation_id = ? AND user_id != ?`,
		conversationID, viewerID).Scan(&othersMinRead, &othersMinDelivered)

	//pull everything display on screen
	rows, err := db.Query(`
		SELECT messages.id, messages.sender_id, users.username, messages.content,
		       messages.created_at, messages.deleted_at,
		       messages.attachment_path, messages.attachment_name, messages.attachment_type
		FROM messages
		JOIN users ON users.id = messages.sender_id
		WHERE messages.conversation_id = ?
		ORDER BY messages.id ASC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []ChatMessage
	for rows.Next() {
		var m ChatMessage
		var senderID int
		var deletedAt sql.NullTime
		var attachPath, attachName, attachType sql.NullString
		if err := rows.Scan(&m.ID, &senderID, &m.Username, &m.Content, &m.CreatedAt,
			&deletedAt, &attachPath, &attachName, &attachType); err != nil {
			return nil, err
		}
		m.Mine = senderID == viewerID
		m.Deleted = deletedAt.Valid
		m.Read = m.Mine && m.ID <= othersMinRead
		m.Delivered = m.Mine && m.ID <= othersMinDelivered
		m.AttachmentPath = attachPath.String
		m.AttachmentName = attachName.String
		m.AttachmentType = attachType.String
		if m.Deleted {
			m.Content = "This message was deleted"
			m.AttachmentPath = ""
		}
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

// _______DELETE SMS_______//
func deleteMessageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := currentUser(r)
	if userID == 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if !checkCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}

	// convert string into proper int (strconv)
	messageID, err := strconv.Atoi(r.FormValue("message_id"))
	if err != nil {
		http.Error(w, "message not found", http.StatusNotFound)
		return
	}

	var conversationID int
	result, err := db.Exec(
		"UPDATE messages SET deleted_at = NOW() WHERE id = ? AND sender_id = ? AND deleted_at IS NULL",
		messageID, userID,
	)
	if err != nil {
		http.Error(w, "could not delete message", http.StatusInternalServerError)
		return
	}

	rowsChanged, err := result.RowsAffected()
	if err != nil {
		http.Error(w, "could not delete message", http.StatusInternalServerError)
		return
	}
	if rowsChanged == 0 {
		http.Error(w, "message not found", http.StatusNotFound)
		return
	}

	db.QueryRow("SELECT conversation_id FROM messages WHERE id = ?", messageID).Scan(&conversationID)
	http.Redirect(w, r, "/dashboard?c="+strconv.Itoa(conversationID), http.StatusSeeOther)
}

func deleteConversationsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := currentUser(r)
	if userID == 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if !checkCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid conversation selection", http.StatusBadRequest)
		return
	}

	conversationIDs := make([]int, 0, len(r.PostForm["conversation_id"]))
	seen := make(map[int]struct{}, len(r.PostForm["conversation_id"]))
	for _, value := range r.PostForm["conversation_id"] {
		conversationID, err := strconv.Atoi(value)
		if err != nil || conversationID <= 0 {
			http.Error(w, "invalid conversation selection", http.StatusBadRequest)
			return
		}
		if _, exists := seen[conversationID]; exists {
			continue
		}
		seen[conversationID] = struct{}{}
		conversationIDs = append(conversationIDs, conversationID)
	}
	if len(conversationIDs) == 0 {
		http.Error(w, "select at least one conversation", http.StatusBadRequest)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "could not delete conversations", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	for _, conversationID := range conversationIDs {
		var one int
		err := tx.QueryRow(
			"SELECT 1 FROM participants WHERE conversation_id = ? AND user_id = ?",
			conversationID, userID,
		).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "conversation not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "could not delete conversations", http.StatusInternalServerError)
			return
		}

		_, err = tx.Exec(
			"INSERT IGNORE INTO hidden_conversations (user_id, conversation_id) VALUES (?, ?)",
			userID, conversationID,
		)
		if err != nil {
			http.Error(w, "could not delete conversations", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "could not delete conversations", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// ______STORE SMS_____//
func saveMessage(conversationID, senderID int, content, attachPath, attachName, attachType string, attachSize int) error {
	_, err := db.Exec(
		`INSERT INTO messages (conversation_id, sender_id, content, attachment_path, attachment_name, attachment_type, attachment_size)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		conversationID, senderID, content, attachPath, attachName, attachType, attachSize,
	)
	return err
}

// ______RETRIVE CHATS_____//
func getConversations(userID int) ([]ConversationSummary, error) {
	rows, err := db.Query(`
		SELECT
		    c.id,
		    c.type,
		    CASE WHEN c.type = 'group' THEN c.name ELSE other_user.username END AS display_name,
		    (SELECT content FROM messages m
		     WHERE m.conversation_id = c.id ORDER BY m.id DESC LIMIT 1) AS last_message,
		    (SELECT created_at FROM messages m
		     WHERE m.conversation_id = c.id ORDER BY m.id DESC LIMIT 1) AS last_time,
		    (SELECT u2.username FROM messages m2
		     JOIN users u2 ON u2.id = m2.sender_id
		     WHERE m2.conversation_id = c.id ORDER BY m2.id DESC LIMIT 1) AS last_sender,
		    (SELECT COUNT(*) FROM messages m3
		     WHERE m3.conversation_id = c.id
		       AND m3.id > me.last_read_message_id
		       AND m3.sender_id != me.user_id) AS unread
		FROM conversations c
		JOIN participants me ON me.conversation_id = c.id AND me.user_id = ?
		LEFT JOIN hidden_conversations hidden
		    ON hidden.conversation_id = c.id AND hidden.user_id = me.user_id
		LEFT JOIN participants other
		    ON other.conversation_id = c.id AND other.user_id != me.user_id AND c.type = 'direct'
		LEFT JOIN users other_user ON other_user.id = other.user_id
		WHERE hidden.conversation_id IS NULL
		ORDER BY last_time DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var conversations []ConversationSummary
	for rows.Next() {
		var c ConversationSummary
		if err := rows.Scan(&c.ID, &c.Type, &c.ChatWith, &c.LastMessage, &c.LastTime, &c.LastSender, &c.Unread); err != nil {
			return nil, err
		}
		conversations = append(conversations, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return conversations, nil
}

func markAsRead(conversationID, userID int) error {
	_, err := db.Exec(`
		UPDATE participants
		SET last_read_message_id = (
		    SELECT COALESCE(MAX(id), 0) FROM messages WHERE conversation_id = ?
		)
		WHERE conversation_id = ? AND user_id = ?`,
		conversationID, conversationID, userID)
	return err
}

func markDelivered(userID int) error {
	_, err := db.Exec(`
		UPDATE participants p
		SET p.last_delivered_message_id = (
		    SELECT COALESCE(MAX(m.id), 0) FROM messages m
		    WHERE m.conversation_id = p.conversation_id
		)
		WHERE p.user_id = ?`, userID)
	return err
}

// _______ SEARCH SMS________//
func searchMessages(conversationID, viewerID int, term string) ([]ChatMessage, error) {
	escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(term)
	pattern := "%" + escaped + "%"

	rows, err := db.Query(`
		SELECT messages.id, messages.sender_id, users.username, messages.content, messages.created_at
		FROM messages
		JOIN users ON users.id = messages.sender_id
		WHERE messages.conversation_id = ?
		  AND messages.deleted_at IS NULL
		  AND messages.content LIKE ?
		ORDER BY messages.id ASC`, conversationID, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []ChatMessage
	for rows.Next() {
		var m ChatMessage
		var senderID int
		if err := rows.Scan(&m.ID, &senderID, &m.Username, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Mine = senderID == viewerID
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

// _____START CHAT___//
func startChatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := currentUser(r)
	if userID == 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if !checkCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	// clear invisible space trim
	username := strings.TrimSpace(r.FormValue("username"))

	var otherID int
	err := db.QueryRow("SELECT id FROM users WHERE username = ?", username).Scan(&otherID)
	if err != nil {
		http.Redirect(w, r, "/dashboard?error=No+user+with+that+username", http.StatusSeeOther)
		return
	}
	if otherID == userID {
		http.Redirect(w, r, "/dashboard?error=You+cannot+start+a+chat+with+yourself", http.StatusSeeOther)
		return
	}

	conversationID, err := findOrCreateDirect(userID, otherID)
	if err != nil {
		http.Error(w, "could not start chat", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/dashboard?c="+strconv.Itoa(conversationID), http.StatusSeeOther)
}

// ____ saves a new message into an existing conversation____//
func postMessageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := currentUser(r)
	if userID == 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if !checkCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}

	conversationID, err := strconv.Atoi(r.FormValue("conversation_id"))
	if err != nil || !isParticipant(conversationID, userID) {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}

	back := "/dashboard?c=" + strconv.Itoa(conversationID)

	content := strings.TrimSpace(r.FormValue("content"))

	var attachPath, attachName, attachType string
	var attachSize int

	filename, contentType, size, uploadErr := saveUploadedFile(r)
	if uploadErr == nil {
		attachPath = filename
		attachType = contentType
		attachSize = int(size)
		if _, header, err := r.FormFile("attachment"); err == nil {
			attachName = header.Filename
		}

	} else if !errors.Is(uploadErr, http.ErrMissingFile) {
		http.Redirect(w, r, back+"&error="+uploadErr.Error(), http.StatusSeeOther)
		return
	}

	if content == "" && attachPath == "" {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}

	if utf8.RuneCountInString(content) > maxMessageLength {
		http.Redirect(w, r, back+"&error=Message+is+too+long+(max+500+characters)", http.StatusSeeOther)
		return
	}

	if err := saveMessage(conversationID, userID, content, attachPath, attachName, attachType, attachSize); err != nil {
		http.Error(w, "could not save message", http.StatusInternalServerError)
		return
	}

	senderName := ""
	if err := db.QueryRow("SELECT username FROM users WHERE id = ?", userID).Scan(&senderName); err != nil {
		log.Println("post message: could not load sender name:", err)
	}

	// Tell every open page in this conversation about the new message so it
	// can refresh the thread immediately (the WebSocket used to carry only
	// typing/presence, which is why messages needed a manual reload).
	preview := strings.Join(strings.Fields(content), " ")
	if preview == "" {
		preview = "Sent an attachment"
	}
	if runes := []rune(preview); len(runes) > 80 {
		preview = string(runes[:80]) + "..."
	}
	broadcastConversationMessage(conversationID, userID, senderName, preview)

	members, err := otherMemberIDs(conversationID, userID)
	if err == nil {
		for _, memberID := range members {
			if !isOnline(memberID) {
				go sendPushToUser(memberID, senderName, "Sent you a new message", "/dashboard?c="+strconv.Itoa(conversationID))
			}
		}
	}

	http.Redirect(w, r, back, http.StatusSeeOther)
}

// _____ CHAT LIST___//
func conversationPartners(userID int) ([]int, error) {
	rows, err := db.Query(`
		SELECT DISTINCT other.user_id
		FROM participants me
		JOIN participants other ON other.conversation_id = me.conversation_id
		WHERE me.user_id = ? AND other.user_id != ?`,
		userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var partners []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		partners = append(partners, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return partners, nil
}

// otherParticipantID returns the user ID of the other person in a
// one-to-one chat, or 0 if it cannot be found.
func otherParticipantID(conversationID, userID int) int {
	var id int
	db.QueryRow(`
		SELECT user_id FROM participants
		WHERE conversation_id = ? AND user_id != ?
		LIMIT 1`, conversationID, userID).Scan(&id)
	return id
}

// _____CREATE GROUP_____//
func createGroup(creatorID int, name string, memberIDs []int) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		"INSERT INTO conversations (type, name, created_by) VALUES ('group', ?, ?)",
		name, creatorID,
	)
	if err != nil {
		return 0, err
	}
	newID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	// Build the member list: the creator, plus everyone they picked,
	// with no duplicates.
	allMembers := []int{creatorID}
	for _, id := range memberIDs {
		if id != creatorID {
			allMembers = append(allMembers, id)
		}
	}

	placeholders := make([]string, len(allMembers))
	args := make([]interface{}, 0, len(allMembers)*2)
	for i, memberID := range allMembers {
		placeholders[i] = "(?, ?)"
		args = append(args, newID, memberID)
	}

	query := "INSERT INTO participants (conversation_id, user_id) VALUES " +
		strings.Join(placeholders, ", ")

	if _, err := tx.Exec(query, args...); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(newID), nil
}

func isGroupCreator(conversationID, userID int) bool {
	var createdBy sql.NullInt64
	db.QueryRow(
		"SELECT created_by FROM conversations WHERE id = ?",
		conversationID,
	).Scan(&createdBy)
	return createdBy.Valid && int(createdBy.Int64) == userID
}

// conversationInfo returns a conversation's type ("direct" or "group"),
// its group name (empty for direct chats), and how many members it has.
func conversationInfo(conversationID int) (convType, name string, memberCount int) {
	var nameCol sql.NullString
	db.QueryRow(
		"SELECT type, name FROM conversations WHERE id = ?",
		conversationID,
	).Scan(&convType, &nameCol)
	name = nameCol.String

	db.QueryRow(
		"SELECT COUNT(*) FROM participants WHERE conversation_id = ?",
		conversationID,
	).Scan(&memberCount)
	return
}

// otherMemberIDs returns the user IDs of everyone in a conversation
// except the given user.
func otherMemberIDs(conversationID, userID int) ([]int, error) {
	rows, err := db.Query(`
		SELECT user_id FROM participants
		WHERE conversation_id = ? AND user_id != ?`,
		conversationID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// fetch list by name
func getGroupMembers(conversationID int) ([]GroupMember, error) {
	rows, err := db.Query(`
		SELECT users.id, users.username
		FROM participants
		JOIN users ON users.id = participants.user_id
		WHERE participants.conversation_id = ?
		ORDER BY users.username ASC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []GroupMember
	for rows.Next() {
		var m GroupMember
		if err := rows.Scan(&m.ID, &m.Username); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return members, nil
}

// _____LEFT GROUP_____//
func leaveGroup(conversationID, userID int) error {
	convType, _, _ := conversationInfo(conversationID)
	if convType != "group" {
		return errors.New("not a group")
	}

	_, err := db.Exec(
		"DELETE FROM participants WHERE conversation_id = ? AND user_id = ?",
		conversationID, userID,
	)
	return err
}

func addGroupMember(conversationID int, username string) error {
	convType, _, _ := conversationInfo(conversationID)
	if convType != "group" {
		return errors.New("not a group")
	}

	var newUserID int
	err := db.QueryRow("SELECT id FROM users WHERE username = ?", username).Scan(&newUserID)
	if err != nil {
		return errors.New("no user with that username")
	}

	if isParticipant(conversationID, newUserID) {
		return errors.New("already a member")
	}

	_, err = db.Exec(
		"INSERT INTO participants (conversation_id, user_id) VALUES (?, ?)",
		conversationID, newUserID,
	)
	return err
}

// only admin can remove group member
func removeGroupMember(conversationID, requesterID, targetUserID int) error {
	if !isGroupCreator(conversationID, requesterID) {
		return errors.New("only the creator can remove members")
	}
	if targetUserID == requesterID {
		return errors.New("use leave group instead of removing yourself")
	}
	if !isParticipant(conversationID, targetUserID) {
		return errors.New("that user is not a member")
	}

	_, err := db.Exec(
		"DELETE FROM participants WHERE conversation_id = ? AND user_id = ?",
		conversationID, targetUserID,
	)
	return err
}

// ___SEARCH FRAGMENT___//
func searchFragmentHandler(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	if userID == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conversationID, err := strconv.Atoi(r.URL.Query().Get("c"))
	if err != nil || !isParticipant(conversationID, userID) {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}

	term := strings.TrimSpace(r.URL.Query().Get("search"))

	var messages []ChatMessage
	if term == "" {
		// The live-update path calls this endpoint with no search term, so a
		// message that just arrived in the open thread is marked as read.
		if err := markAsRead(conversationID, userID); err != nil {
			log.Println("search fragment: could not mark as read:", err)
		}
		messages, err = getConversationMessages(conversationID, userID)
	} else {
		messages, err = searchMessages(conversationID, userID, term)
	}
	if err != nil {
		http.Error(w, "could not load messages", http.StatusInternalServerError)
		return
	}

	convType, _, _ := conversationInfo(conversationID)
	data := MessagesFragmentData{
		Messages:         messages,
		ConversationType: convType,
		CSRFToken:        csrfCookieValue(r),
	}
	renderTemplate(w, "messages_fragment.html", data)
}
