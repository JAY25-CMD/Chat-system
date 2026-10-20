package main

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// onlineUsers tracks which user IDs currently have an open WebSocket
var (
	connections = make(map[int]*websocket.Conn)
	connMu      sync.Mutex
)

func registerConnection(userID int, conn *websocket.Conn) {
	connMu.Lock()
	defer connMu.Unlock()
	connections[userID] = conn
}

func removeConnection(userID int) {
	connMu.Lock()
	defer connMu.Unlock()
	delete(connections, userID)
}

func isOnline(userID int) bool {
	connMu.Lock()
	defer connMu.Unlock()
	_, ok := connections[userID]
	return ok
}

const (
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
)

// ______WEBSOCKET______//
type incomingMessage struct {
	Type           string `json:"type"`
	ConversationID int    `json:"conversation_id"`
}

func wsEchoHandler(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	if userID == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade error:", err)
		return
	}
	defer conn.Close()

	registerConnection(userID, conn)
	defer removeConnection(userID)

	broadcastPresence(userID, true)
	defer broadcastPresence(userID, false)

	log.Println("WebSocket connected for user", userID)

	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	go pingLoop(conn)

	for {
		var msg incomingMessage
		if err := conn.ReadJSON(&msg); err != nil {
			log.Println("connection closed:", err)
			break
		}

		if msg.Type == "typing" {
			handleTyping(userID, msg.ConversationID)
		}
	}
}

// ______TYPING SMS_____//
func handleTyping(userID, conversationID int) {
	if !isParticipant(conversationID, userID) {
		return
	}

	members, err := otherMemberIDs(conversationID, userID)
	if err != nil {
		log.Println("otherMemberIDs error:", err)
		return
	}

	payload := map[string]interface{}{
		"type":            "typing",
		"conversation_id": conversationID,
		"user_id":         userID,
	}

	for _, memberID := range members {
		notifyUser(memberID, payload)
	}
}

// pingLoop sends a ping frame at regular intervals to detect dead
// connections that never sent a proper close message.
func pingLoop(conn *websocket.Conn) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for range ticker.C {
		if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
			return
		}
	}
}

// _____ online status to everyone___/
func broadcastPresence(userID int, online bool) {
	partners, err := conversationPartners(userID)
	if err != nil {
		log.Println("conversationPartners error:", err)
		return
	}

	payload := map[string]interface{}{
		"type":    "presence",
		"user_id": userID,
		"online":  online,
	}

	for _, partnerID := range partners {
		notifyUser(partnerID, payload)
	}
}

// notifyUser sends a JSON payload to one user's open connection
func notifyUser(userID int, payload interface{}) {
	connMu.Lock()
	conn, ok := connections[userID]
	connMu.Unlock()

	if !ok {
		return
	}

	if err := conn.WriteJSON(payload); err != nil {
		log.Println("notify error:", err)
	}
}
