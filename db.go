package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"

	_ "github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
)

var db *sql.DB

func initDB() {

	err := godotenv.Load()
	if err != nil {
		log.Fatal("could not load .env file: ", err)
	}

	dbUser := os.Getenv("DB_USER")
	dbPassword := os.Getenv("DB_PASSWORD")
	dbHost := os.Getenv("DB_HOST")
	dbPort := os.Getenv("DB_PORT")
	dbName := os.Getenv("DB_NAME")

	connectionString := dbUser + ":" + dbPassword + "@tcp(" + dbHost + ":" + dbPort + ")/" + dbName + "?parseTime=true"

	connection, err := sql.Open("mysql", connectionString)
	if err != nil {
		log.Fatal("failed to open database: ", err)
	}

	if err = connection.Ping(); err != nil {
		log.Fatal("failed to connect to database: ", err)
	}

	_, err = connection.Exec(`
		CREATE TABLE IF NOT EXISTS hidden_conversations (
			user_id INT NOT NULL,
			conversation_id INT NOT NULL,
			PRIMARY KEY (user_id, conversation_id)
		)`)
	if err != nil {
		log.Fatal("failed to initialize hidden conversations: ", err)
	}

	db = connection
	fmt.Println("Successfully connected to MariaDB")
}
