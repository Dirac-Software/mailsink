package main

import (
	"bytes"
	"crypto/tls"
	"database/sql"
	"embed"
	"encoding/json"
	"flag"
	"html/template"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/buildkite/terminal-to-html/v3"
	"github.com/chrj/smtpd"
	"github.com/microcosm-cc/bluemonday"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed templates/*
var templatesFS embed.FS

type Email struct {
	ID            int64     `json:"id"`
	From          string    `json:"from"`
	To            string    `json:"to"`
	Subject       string    `json:"subject"`
	Body          string    `json:"body"`
	HTML          string    `json:"html"`
	SanitizedHTML string    `json:"sanitizedHtml"`
	AnsiHTML      string    `json:"ansiHtml"`
	Raw           string    `json:"raw"`
	Timestamp     time.Time `json:"timestamp"`
	ContentType   string    `json:"contentType"`
	HasAnsi       bool      `json:"hasAnsi"`
}

var (
	db            *sql.DB
	htmlSanitizer *bluemonday.Policy
	forwardAddr   string
	useStartTLS   bool
)

func initSanitizer() {
	htmlSanitizer = bluemonday.UGCPolicy()
	htmlSanitizer.AllowAttrs("style").OnElements("p", "div", "span", "h1", "h2", "h3", "h4", "h5", "h6")
	htmlSanitizer.AllowStyles("color", "background-color", "font-weight", "font-style", "text-decoration", "text-align").Globally()
}

func hasAnsiCodes(text string) bool {
	// Check for ANSI escape sequences
	return strings.Contains(text, "\x1b[")
}

func convertAnsiToHTML(text string) string {
	// Convert ANSI codes to HTML
	html := string(terminal.Render([]byte(text)))
	
	// The terminal library doesn't preserve line breaks, so we need to ensure they're converted to <br>
	// Replace newlines with <br> tags while preserving the ANSI-converted HTML
	html = strings.ReplaceAll(html, "\n", "<br>")
	
	return html
}

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

func forwardEmail(from string, recipients []string, data []byte) error {
	// Connect to the SMTP server
	c, err := smtp.Dial(forwardAddr)
	if err != nil {
		return err
	}
	defer c.Close()

	// Use STARTTLS if enabled
	if useStartTLS {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         strings.Split(forwardAddr, ":")[0],
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return err
		}
	}

	// Set the sender
	if err := c.Mail(from); err != nil {
		return err
	}

	// Set the recipients
	for _, recipient := range recipients {
		if err := c.Rcpt(recipient); err != nil {
			return err
		}
	}

	// Send the email body
	wc, err := c.Data()
	if err != nil {
		return err
	}
	defer wc.Close()

	_, err = wc.Write(data)
	if err != nil {
		return err
	}

	return nil
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

	// Forward email if forwarding is enabled
	if forwardAddr != "" {
		if err := forwardEmail(from, env.Recipients, data); err != nil {
			log.Printf("Failed to forward email: %v", err)
		} else {
			log.Printf("Email forwarded to %s", forwardAddr)
		}
	}

	return nil
}

func parseEmail(raw string) (subject, body, html string) {
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return parseEmailSimple(raw)
	}

	if msg.Header != nil {
		subject = msg.Header.Get("Subject")
	}

	contentType := msg.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(msg.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}

			partContentType := part.Header.Get("Content-Type")
			partMediaType, _, _ := mime.ParseMediaType(partContentType)

			buf := new(bytes.Buffer)
			_, _ = buf.ReadFrom(part)
			content := buf.String()

			if partMediaType == "text/plain" && body == "" {
				body = strings.TrimSpace(content)
			} else if partMediaType == "text/html" && html == "" {
				html = strings.TrimSpace(content)
			}
		}
	} else {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(msg.Body)
		content := buf.String()

		if mediaType == "text/html" {
			html = strings.TrimSpace(content)
		} else {
			body = strings.TrimSpace(content)
		}
	}

	return subject, body, html
}

func parseEmailSimple(raw string) (subject, body, html string) {
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
		
		// Determine content type and process content
		if e.HTML != "" {
			e.ContentType = "text/html"
			e.SanitizedHTML = htmlSanitizer.Sanitize(e.HTML)
		} else {
			e.ContentType = "text/plain"
			// Check for ANSI codes in plain text
			if hasAnsiCodes(e.Body) {
				e.HasAnsi = true
				e.AnsiHTML = convertAnsiToHTML(e.Body)
			}
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
	
	// Determine content type and process content
	if e.HTML != "" {
		e.ContentType = "text/html"
		e.SanitizedHTML = htmlSanitizer.Sanitize(e.HTML)
	} else {
		e.ContentType = "text/plain"
		// Check for ANSI codes in plain text
		if hasAnsiCodes(e.Body) {
			e.HasAnsi = true
			e.AnsiHTML = convertAnsiToHTML(e.Body)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(e)
}

func main() {
	var (
		smtpAddr = flag.String("smtp", "127.0.0.1:2525", "SMTP server address")
		httpAddr = flag.String("http", "127.0.0.1:8080", "HTTP server address")
		dbPath   = flag.String("db", "mailsink.db", "SQLite database path")
		forward  = flag.String("forward", "", "Forward emails to hostname or hostname:port (default port 25)")
		starttls = flag.Bool("starttls", false, "Use STARTTLS when forwarding (without certificate verification)")
	)
	flag.Parse()

	// Parse forward address and add default port if needed
	if *forward != "" {
		if !strings.Contains(*forward, ":") {
			forwardAddr = *forward + ":25"
		} else {
			forwardAddr = *forward
		}
		useStartTLS = *starttls
		if useStartTLS {
			log.Printf("Email forwarding enabled to %s with STARTTLS (no cert verification)", forwardAddr)
		} else {
			log.Printf("Email forwarding enabled to %s", forwardAddr)
		}
	}

	initSanitizer()
	
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