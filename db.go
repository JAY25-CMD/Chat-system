package main

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
)

var db *sql.DB

// The schema lives in schema.sql so the same file can be applied by hand
// (Layerbase SQL console, phpMyAdmin, mysql CLI) and automatically at startup.
//
//go:embed schema.sql
var schemaSQL string

var requiredTables = []string{
	"users",
	"sessions",
	"conversations",
	"participants",
	"messages",
	"hidden_conversations",
	"push_subscriptions",
}

func initDB() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using system environment variables")
	}

	dsn, err := databaseDSN()
	if err != nil {
		log.Fatal("database configuration error: ", err)
	}

	connection, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal("failed to open database: ", err)
	}

	// Managed databases close idle connections after a few minutes. Keeping
	// connections short-lived avoids the "invalid connection" errors that show
	// up as random login failures in production.
	connection.SetMaxOpenConns(15)
	connection.SetMaxIdleConns(5)
	connection.SetConnMaxLifetime(3 * time.Minute)
	connection.SetConnMaxIdleTime(time.Minute)

	if err := connection.Ping(); err != nil {
		log.Fatalf("failed to connect to database: %v (check DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME or DATABASE_URL)", err)
	}

	if err := ensureSchema(connection); err != nil {
		log.Fatal("database schema error: ", err)
	}

	db = connection

	var currentDB sql.NullString
	if err := connection.QueryRow("SELECT DATABASE()").Scan(&currentDB); err != nil {
		log.Println("could not read the active database name:", err)
	}
	log.Printf("connected to MySQL, database %q, schema ready", currentDB.String)
}

// databaseDSN builds the MySQL DSN from either a single connection URL
// (DATABASE_URL / MYSQL_URL, commonly provided by hosting platforms) or the
// individual DB_* variables. Building it with mysql.Config means passwords
// containing @, :, /, # or ? are escaped correctly.
func databaseDSN() (string, error) {
	user := strings.TrimSpace(os.Getenv("DB_USER"))
	password := os.Getenv("DB_PASSWORD")
	host := strings.TrimSpace(os.Getenv("DB_HOST"))
	port := strings.TrimSpace(os.Getenv("DB_PORT"))
	name := strings.TrimSpace(os.Getenv("DB_NAME"))

	// Prefer the explicit DB_* variables when they are present. A platform may
	// also inject an unrelated DATABASE_URL (for example a Postgres add-on),
	// so only fall back to the URL when the individual variables are absent.
	if user == "" && host == "" && name == "" {
		if raw := firstEnv("DATABASE_URL", "MYSQL_URL"); raw != "" {
			return dsnFromURL(raw)
		}
	}

	var missing []string
	if user == "" {
		missing = append(missing, "DB_USER")
	}
	if host == "" {
		missing = append(missing, "DB_HOST")
	}
	if name == "" {
		missing = append(missing, "DB_NAME")
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("missing environment variables: %s (set them in the hosting dashboard, not only in a local .env file; DATABASE_URL or MYSQL_URL can be used instead)", strings.Join(missing, ", "))
	}

	if port == "" {
		// Some hosts store "hostname:port" in DB_HOST instead of DB_PORT.
		if h, p, err := net.SplitHostPort(host); err == nil {
			host, port = h, p
		} else {
			port = "3306"
		}
	}

	cfg := mysql.NewConfig()
	cfg.User = user
	cfg.Passwd = password
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(host, port)
	cfg.DBName = name
	if tls := strings.TrimSpace(os.Getenv("DB_TLS")); tls != "" {
		cfg.TLSConfig = tls
	}
	applyDSNDefaults(cfg)

	return cfg.FormatDSN(), nil
}

func dsnFromURL(raw string) (string, error) {
	// A plain driver DSN such as user:pass@tcp(host:3306)/db is accepted too.
	if !strings.Contains(raw, "://") {
		cfg, err := mysql.ParseDSN(raw)
		if err != nil {
			return "", fmt.Errorf("invalid database URL/DSN: %w", err)
		}
		applyDSNDefaults(cfg)
		return cfg.FormatDSN(), nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid database URL: %w", err)
	}

	switch strings.ToLower(u.Scheme) {
	case "mysql", "mariadb":
	default:
		return "", fmt.Errorf("database URL must start with mysql:// or mariadb://, got %q", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return "", errors.New("database URL is missing a host")
	}
	port := u.Port()
	if port == "" {
		port = "3306"
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" {
		return "", errors.New("database URL is missing a database name")
	}

	cfg := mysql.NewConfig()
	if u.User != nil {
		cfg.User = u.User.Username()
		cfg.Passwd, _ = u.User.Password()
	}
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(host, port)
	cfg.DBName = name
	if tls := u.Query().Get("tls"); tls != "" {
		cfg.TLSConfig = tls
	}
	applyDSNDefaults(cfg)

	return cfg.FormatDSN(), nil
}

func applyDSNDefaults(cfg *mysql.Config) {
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 30 * time.Second
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 30 * time.Second
	}
	// Some managed MySQL endpoints (and connection poolers in front of them)
	// do not implement server-side prepared statements. Interpolating the
	// parameters client-side keeps the "?" placeholders working everywhere.
	cfg.InterpolateParams = true
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func ensureSchema(connection *sql.DB) error {
	statements := splitSQLStatements(schemaSQL)
	if len(statements) == 0 {
		return errors.New("embedded schema.sql contains no statements")
	}

	for i, statement := range statements {
		if _, err := connection.Exec(statement); err != nil {
			return fmt.Errorf("applying schema statement %d (%s): %w", i+1, firstLine(statement), err)
		}
	}

	return verifySchema(connection)
}

// splitSQLStatements turns schema.sql into individual statements. Comment
// lines starting with "--" are ignored, and each statement must end with ";".
func splitSQLStatements(script string) []string {
	var (
		statements []string
		current    strings.Builder
	)

	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}

		current.WriteString(line)
		current.WriteString("\n")

		if strings.HasSuffix(trimmed, ";") {
			statement := strings.TrimSpace(current.String())
			statement = strings.TrimSuffix(statement, ";")
			if strings.TrimSpace(statement) != "" {
				statements = append(statements, statement)
			}
			current.Reset()
		}
	}

	if rest := strings.TrimSpace(current.String()); rest != "" {
		statements = append(statements, rest)
	}

	return statements
}

func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return strings.TrimSpace(text)
}

func verifySchema(connection *sql.DB) error {
	rows, err := connection.Query(
		"SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE()",
	)
	if err != nil {
		return fmt.Errorf("reading database schema: %w", err)
	}
	defer rows.Close()

	existing := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		existing[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	var missing []string
	for _, table := range requiredTables {
		if !existing[table] {
			missing = append(missing, table)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("database is missing tables: %s (apply schema.sql to the production database)", strings.Join(missing, ", "))
	}

	checkPasswordHashColumn(connection)
	return nil
}

// checkPasswordHashColumn catches the classic production-only auth bug: a
// password_hash column too short for bcrypt. Registration then either fails or
// stores a truncated hash, and every login says "invalid username or password".
func checkPasswordHashColumn(connection *sql.DB) {
	var (
		dataType  string
		maxLength sql.NullInt64
	)
	err := connection.QueryRow(`
		SELECT DATA_TYPE, CHARACTER_MAXIMUM_LENGTH
		FROM information_schema.columns
		WHERE table_schema = DATABASE()
		  AND table_name = 'users'
		  AND column_name = 'password_hash'`).Scan(&dataType, &maxLength)
	if errors.Is(err, sql.ErrNoRows) {
		log.Println("WARNING: users.password_hash column was not found; apply schema.sql")
		return
	}
	if err != nil {
		log.Println("WARNING: could not inspect users.password_hash column:", err)
		return
	}

	if strings.EqualFold(dataType, "varchar") && maxLength.Valid && maxLength.Int64 < 60 {
		log.Printf(
			"WARNING: users.password_hash is VARCHAR(%d), but bcrypt hashes are 60 characters. "+
				"New accounts and logins will fail. Run: ALTER TABLE users MODIFY password_hash VARCHAR(255) NOT NULL;",
			maxLength.Int64,
		)
	}
}
