package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"flag"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/chrj/smtpd"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed templates/*
var templatesFS embed.FS

type Email struct {
	ID        int64     `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body"`
	HTML      string    `json:"html"`
	Raw       string    `json:"raw"`
	Timestamp time.Time `json:"timestamp"`
}

var (
	db *sql.DB
)

func initDB(dbPath string) error {
	var err error
	db, err = sql.Open("sqlite3", dbPath)
	if err != nil {
		return err
	}

	schema := `
	CREATE TABLE IF NOT EXISTS emails (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		from_addr TEXT NOT NULL,
		to_addr TEXT NOT NULL,
		subject TEXT,
		body TEXT,
		html TEXT,
		raw TEXT,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE VIRTUAL TABLE IF NOT EXISTS emails_fts USING fts5(
		subject, body, from_addr, to_addr,
		content=emails,
		content_rowid=id
	);

	CREATE TRIGGER IF NOT EXISTS emails_ai AFTER INSERT ON emails BEGIN
		INSERT INTO emails_fts(rowid, subject, body, from_addr, to_addr)
		VALUES (new.id, new.subject, new.body, new.from_addr, new.to_addr);
	END;

	CREATE TRIGGER IF NOT EXISTS emails_ad AFTER DELETE ON emails BEGIN
		DELETE FROM emails_fts WHERE rowid = old.id;
	END;

	CREATE TRIGGER IF NOT EXISTS emails_au AFTER UPDATE ON emails BEGIN
		DELETE FROM emails_fts WHERE rowid = old.id;
		INSERT INTO emails_fts(rowid, subject, body, from_addr, to_addr)
		VALUES (new.id, new.subject, new.body, new.from_addr, new.to_addr);
	END;
	`

	_, err = db.Exec(schema)
	return err
}

func mailHandler(peer smtpd.Peer, env smtpd.Envelope) error {
	from := env.Sender
	recipients := strings.Join(env.Recipients, ", ")
	
	data := env.Data

	rawEmail := string(data)
	subject, body, html := parseEmail(rawEmail)

	_, err := db.Exec(`
		INSERT INTO emails (from_addr, to_addr, subject, body, html, raw)
		VALUES (?, ?, ?, ?, ?, ?)
	`, from, recipients, subject, body, html, rawEmail)

	if err != nil {
		return err
	}

	log.Printf("Email received from %s to %s", from, recipients)
	return nil
}

func parseEmail(raw string) (subject, body, html string) {
	lines := strings.Split(raw, "\n")
	inBody := false
	isHTML := false
	
	for _, line := range lines {
		if !inBody {
			if strings.HasPrefix(line, "Subject: ") {
				subject = strings.TrimPrefix(line, "Subject: ")
			} else if strings.Contains(line, "Content-Type: text/html") {
				isHTML = true
			} else if line == "" {
				inBody = true
			}
		} else {
			if isHTML {
				html += line + "\n"
			} else {
				body += line + "\n"
			}
		}
	}
	
	return strings.TrimSpace(subject), strings.TrimSpace(body), strings.TrimSpace(html)
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFS(templatesFS, "templates/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := struct {
		Title string
	}{
		Title: "MailSink",
	}

	err = tmpl.Execute(w, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func emailsHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	var rows *sql.Rows
	var err error

	if query != "" {
		// Convert Google-like query to FTS5 query
		ftsQuery, err := fts5_term(query)
		if err != nil {
			http.Error(w, "Invalid search query: "+err.Error(), http.StatusBadRequest)
			return
		}

		rows, err = db.Query(`
			SELECT e.id, e.from_addr, e.to_addr, e.subject, e.body, e.html, e.raw, e.timestamp
			FROM emails e
			JOIN emails_fts ON e.id = emails_fts.rowid
			WHERE emails_fts MATCH ?
			ORDER BY e.timestamp DESC
			LIMIT 100
		`, ftsQuery)
	} else {
		rows, err = db.Query(`
			SELECT id, from_addr, to_addr, subject, body, html, raw, timestamp
			FROM emails
			ORDER BY timestamp DESC
			LIMIT 100
		`)
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var emails []Email
	for rows.Next() {
		var e Email
		err := rows.Scan(&e.ID, &e.From, &e.To, &e.Subject, &e.Body, &e.HTML, &e.Raw, &e.Timestamp)
		if err != nil {
			continue
		}
		emails = append(emails, e)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(emails)
}

func emailHandler(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/email/")
	
	var e Email
	err := db.QueryRow(`
		SELECT id, from_addr, to_addr, subject, body, html, raw, timestamp
		FROM emails
		WHERE id = ?
	`, id).Scan(&e.ID, &e.From, &e.To, &e.Subject, &e.Body, &e.HTML, &e.Raw, &e.Timestamp)

	if err != nil {
		http.Error(w, "Email not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(e)
}

func main() {
	var (
		smtpAddr = flag.String("smtp", "127.0.0.1:2525", "SMTP server address")
		httpAddr = flag.String("http", "127.0.0.1:8080", "HTTP server address")
		dbPath   = flag.String("db", "mailsink.db", "SQLite database path")
	)
	flag.Parse()

	if err := initDB(*dbPath); err != nil {
		log.Fatal("Failed to initialize database:", err)
	}

	srv := &smtpd.Server{
		Handler:  mailHandler,
		Hostname: "localhost",
		WelcomeMessage: "MailSink ESMTP ready",
	}

	go func() {
		log.Printf("Starting SMTP server on %s", *smtpAddr)
		if err := srv.ListenAndServe(*smtpAddr); err != nil {
			log.Fatal("SMTP server error:", err)
		}
	}()

	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/api/emails", emailsHandler)
	http.HandleFunc("/api/email/", emailHandler)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(templatesFS))))

	log.Printf("Starting HTTP server on %s", *httpAddr)
	if err := http.ListenAndServe(*httpAddr, nil); err != nil {
		log.Fatal("HTTP server error:", err)
	}
}