package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type DashboardData struct {
	Username         string
	Messages         []ChatMessage
	Conversations    []ConversationSummary
	Error            string
	ConversationID   int
	ChatWith         string
	ChatWithID       int
	ChatWithOnline   bool
	ConversationType string
	MemberCount      int
	SearchTerm       string
	VapidPublicKey   string
	CSRFToken        string
}

type MembersPageData struct {
	ConversationID int
	GroupName      string
	Members        []GroupMember
	CreatorID      int
	CurrentUserID  int
	Error          string
	CSRFToken      string
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ________DASHBOARD_______//
func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	if userID == 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := markDelivered(userID); err != nil {
		http.Error(w, "could not update delivery status", http.StatusInternalServerError)
		return
	}

	var username string
	row := db.QueryRow("SELECT username FROM users WHERE id = ?", userID)
	if err := row.Scan(&username); err != nil {
		http.Error(w, "could not load user", http.StatusInternalServerError)
		return
	}

	data := DashboardData{
		Username:  username,
		Error:     r.URL.Query().Get("error"),
		CSRFToken: csrfCookieValue(r),
	}

	// A conversation is open when the address looks like /dashboard?c=5
	if c := r.URL.Query().Get("c"); c != "" {
		conversationID, err := strconv.Atoi(c)
		if err != nil || !isParticipant(conversationID, userID) {
			http.Error(w, "conversation not found", http.StatusNotFound)
			return
		}

		if _, err := db.Exec(
			"DELETE FROM hidden_conversations WHERE user_id = ? AND conversation_id = ?",
			userID, conversationID,
		); err != nil {
			http.Error(w, "could not restore conversation", http.StatusInternalServerError)
			return
		}

		if err := markAsRead(conversationID, userID); err != nil {
			http.Error(w, "could not update read status", http.StatusInternalServerError)
			return
		}

		searchTerm := strings.TrimSpace(r.URL.Query().Get("search"))

		var messages []ChatMessage
		if searchTerm != "" {
			messages, err = searchMessages(conversationID, userID, searchTerm)
		} else {
			messages, err = getConversationMessages(conversationID, userID)
		}
		if err != nil {
			http.Error(w, "could not load messages", http.StatusInternalServerError)
			return
		}

		data.ConversationID = conversationID
		data.Messages = messages
		data.SearchTerm = searchTerm
		convType, groupName, memberCount := conversationInfo(conversationID)
		data.ConversationType = convType
		data.MemberCount = memberCount

		if convType == "group" {
			data.ChatWith = groupName
		} else {
			data.ChatWith = otherParticipantName(conversationID, userID)
			data.ChatWithID = otherParticipantID(conversationID, userID)
			data.ChatWithOnline = isOnline(data.ChatWithID)
		}

	}

	conversations, err := getConversations(userID)
	if err != nil {
		http.Error(w, "could not load conversations", http.StatusInternalServerError)
		return
	}
	data.Conversations = conversations
	data.VapidPublicKey = os.Getenv("VAPID_PUBLIC_KEY")

	renderTemplate(w, "dashboard.html", data)
}

// ______GROUP MEMBERS___//
func groupMembersHandler(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	if userID == 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	conversationID, err := strconv.Atoi(r.URL.Query().Get("c"))
	if err != nil || !isParticipant(conversationID, userID) {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}

	convType, groupName, _ := conversationInfo(conversationID)
	if convType != "group" {
		http.Error(w, "not a group", http.StatusBadRequest)
		return
	}

	members, err := getGroupMembers(conversationID)
	if err != nil {
		http.Error(w, "could not load members", http.StatusInternalServerError)
		return
	}

	creatorID := -1
	var createdBy sql.NullInt64
	db.QueryRow("SELECT created_by FROM conversations WHERE id = ?", conversationID).Scan(&createdBy)
	if createdBy.Valid {
		creatorID = int(createdBy.Int64)
	}

	data := MembersPageData{
		ConversationID: conversationID,
		GroupName:      groupName,
		Members:        members,
		CreatorID:      creatorID,
		CurrentUserID:  userID,
		Error:          r.URL.Query().Get("error"),
		CSRFToken:      csrfCookieValue(r),
	}
	renderTemplate(w, "group_members.html", data)
}

// _____LEFT GROUP_____//
func leaveGroupHandler(w http.ResponseWriter, r *http.Request) {
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

	if err := leaveGroup(conversationID, userID); err != nil {
		http.Error(w, "could not leave group", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// _____ADD GROUP MEMBER____//
func addMemberHandler(w http.ResponseWriter, r *http.Request) {
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

	username := strings.TrimSpace(r.FormValue("username"))
	back := "/group/members?c=" + strconv.Itoa(conversationID)

	if err := addGroupMember(conversationID, username); err != nil {
		http.Redirect(w, r, back+"&error="+err.Error(), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, back, http.StatusSeeOther)
}

// __REMOVE GROUP MEMBER ONLY ADMIN__//
func removeMemberHandler(w http.ResponseWriter, r *http.Request) {
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

	targetUserID, err := strconv.Atoi(r.FormValue("user_id"))
	back := "/group/members?c=" + strconv.Itoa(conversationID)
	if err != nil {
		http.Redirect(w, r, back+"&error=Invalid+member", http.StatusSeeOther)
		return
	}

	if err := removeGroupMember(conversationID, userID, targetUserID); err != nil {
		http.Redirect(w, r, back+"&error="+err.Error(), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, back, http.StatusSeeOther)
}

// _____CREATE GROUP_____//
func createGroupHandler(w http.ResponseWriter, r *http.Request) {
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

	name := strings.TrimSpace(r.FormValue("name"))
	usernamesRaw := r.FormValue("usernames")

	if name == "" {
		http.Redirect(w, r, "/dashboard?error=Group+name+is+required", http.StatusSeeOther)
		return
	}

	var memberIDs []int
	for _, u := range strings.Split(usernamesRaw, ",") {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		var id int
		err := db.QueryRow("SELECT id FROM users WHERE username = ?", u).Scan(&id)
		if err != nil {
			http.Redirect(w, r, "/dashboard?error=No+user+named+"+u, http.StatusSeeOther)
			return
		}
		memberIDs = append(memberIDs, id)
	}

	if len(memberIDs) == 0 {
		http.Redirect(w, r, "/dashboard?error=Add+at+least+one+member", http.StatusSeeOther)
		return
	}

	conversationID, err := createGroup(userID, name, memberIDs)
	if err != nil {
		http.Error(w, "could not create group", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/dashboard?c="+strconv.Itoa(conversationID), http.StatusSeeOther)
}

func main() {
	initDB()
	defer db.Close()

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			log.Println("health check failed:", err)
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	http.HandleFunc("/", rootHandler)
	http.HandleFunc("/register", registerHandler)
	http.HandleFunc("/login", loginHandler)
	http.HandleFunc("/logout", logoutHandler)
	http.HandleFunc("/dashboard", dashboardHandler)
	http.HandleFunc("/chat/start", startChatHandler)
	http.HandleFunc("/postmessage", postMessageHandler)
	http.HandleFunc("/message/delete", deleteMessageHandler)
	http.HandleFunc("/conversation/delete", deleteConversationsHandler)
	http.HandleFunc("/attachment", serveAttachmentHandler)
	http.HandleFunc("/ws", wsEchoHandler)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	http.HandleFunc("/push/subscribe", subscribeHandler)
	http.HandleFunc("/group/members", groupMembersHandler)
	http.HandleFunc("/group/leave", leaveGroupHandler)
	http.HandleFunc("/group/add", addMemberHandler)
	http.HandleFunc("/group/remove", removeMemberHandler)
	http.HandleFunc("/group/create", createGroupHandler)
	http.HandleFunc("/chat/fragment", searchFragmentHandler)

	http.HandleFunc("/debug/online", func(w http.ResponseWriter, r *http.Request) {
		userID := currentUser(r)
		fmt.Fprintln(w, "You are online:", isOnline(userID))
	})

	// Hosting platforms (Render, Layerbase, Railway, ...) tell the app which
	// port to listen on through PORT. Hardcoding 8080 makes the service fail
	// its port scan on Render.
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port

	server := &http.Server{
		Addr:              addr,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	log.Printf("server running at http://localhost%s", addr)
	log.Fatal(server.ListenAndServe())
}
