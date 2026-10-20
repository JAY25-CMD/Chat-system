package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/SherClockHolmes/webpush-go"
)

// subscriptionPayload matches the shape the browser's PushSubscription
type subscriptionPayload struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func subscribeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := currentUser(r)
	if userID == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var sub subscriptionPayload
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
		http.Error(w, "invalid subscription data", http.StatusBadRequest)
		return
	}

	_, err := db.Exec(`
		INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth)
		VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE user_id = VALUES(user_id)`,
		userID, sub.Endpoint, sub.Keys.P256dh, sub.Keys.Auth,
	)
	if err != nil {
		http.Error(w, "could not save subscription", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// ______ ONLY TO OFFLINE USER_____//
func sendPushToUser(userID int, title, body, url string) {
	rows, err := db.Query(
		"SELECT endpoint, p256dh, auth FROM push_subscriptions WHERE user_id = ?",
		userID,
	)
	if err != nil {
		log.Println("push query error:", err)
		return
	}
	defer rows.Close()

	payload, err := json.Marshal(map[string]string{
		"title": title,
		"body":  body,
		"url":   url,
	})
	if err != nil {
		log.Println("push payload error:", err)
		return
	}

	for rows.Next() {
		var endpoint, p256dh, auth string
		if err := rows.Scan(&endpoint, &p256dh, &auth); err != nil {
			log.Println("push scan error:", err)
			continue
		}

		sub := &webpush.Subscription{
			Endpoint: endpoint,
			Keys: webpush.Keys{
				P256dh: p256dh,
				Auth:   auth,
			},
		}

		_, err := webpush.SendNotification(payload, sub, &webpush.Options{
			Subscriber:      "mailto:you@example.com",
			VAPIDPublicKey:  os.Getenv("VAPID_PUBLIC_KEY"),
			VAPIDPrivateKey: os.Getenv("VAPID_PRIVATE_KEY"),
			TTL:             30,
		})
		if err != nil {
			log.Println("push send error:", err)
		}
	}

	if err := rows.Err(); err != nil {
		log.Println("push row error:", err)
	}
}
