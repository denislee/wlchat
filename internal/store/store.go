// store/store.go
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"wlchat/internal/conversation"
)

type Store struct {
	dir       string
	configDir string
	db        *sql.DB
}

func New(dir, configDir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create store directory: %w", err)
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create config directory: %w", err)
	}

	dbPath := filepath.Join(dir, "wlchat.db")
	db, err := sql.Open("sqlite3", dbPath+"?_timeout=5000&_journal=WAL&_sync=NORMAL&_fk=1&_time_format=sqlite&_loc=auto")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := initDB(db); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	s := &Store{dir: dir, configDir: configDir, db: db}
	if err := s.migrateJSON(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: json migration failed: %v\n", err)
	}

	return s, nil
}

func (s *Store) migrateJSON() error {
	// Migrate config.json if it exists
	configPath := filepath.Join(s.configDir, "config.json")
	if _, err := os.Stat(configPath); err == nil {
		file, err := os.Open(configPath)
		if err == nil {
			var cfg Config
			if err := json.NewDecoder(file).Decode(&cfg); err == nil {
				file.Close()
				if err := s.SaveConfig(cfg); err == nil {
					_ = os.Rename(configPath, configPath+".bak")
				}
			} else {
				file.Close()
			}
		}
	}

	// Migrate history.json if it exists
	historyPath := filepath.Join(s.dir, "history.json")
	if _, err := os.Stat(historyPath); err == nil {
		file, err := os.Open(historyPath)
		if err == nil {
			var entries []string
			if err := json.NewDecoder(file).Decode(&entries); err == nil {
				file.Close()
				if err := s.SaveHistory(entries); err == nil {
					_ = os.Rename(historyPath, historyPath+".bak")
				}
			} else {
				file.Close()
			}
		}
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" || e.Name() == "config.json" || e.Name() == "history.json" || strings.HasSuffix(e.Name(), ".bak") {
			continue
		}

		path := filepath.Join(s.dir, e.Name())
		file, err := os.Open(path)
		if err != nil {
			continue
		}

		var conv conversation.Conversation
		if err := json.NewDecoder(file).Decode(&conv); err != nil {
			file.Close()
			continue
		}
		file.Close()

		// Save to SQLite
		if err := s.Save(conv); err == nil {
			// Rename to .bak so we don't migrate it again
			_ = os.Rename(path, path+".bak")
		}
	}
	return nil
}

func initDB(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS conversations (
		id TEXT PRIMARY KEY,
		title TEXT,
		created_at DATETIME,
		mode TEXT,
		messages TEXT
	);

	CREATE TABLE IF NOT EXISTS kv_store (
		key TEXT PRIMARY KEY,
		value TEXT
	);

	CREATE TABLE IF NOT EXISTS history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		entry TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`
	_, err := db.Exec(schema)
	return err
}

func (s *Store) ConfigDir() string {
	return s.configDir
}

func (s *Store) Save(conv conversation.Conversation) error {
	messagesJSON, err := json.Marshal(conv.Messages)
	if err != nil {
		return err
	}
	query := `
		INSERT INTO conversations (id, title, created_at, mode, messages)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			mode = excluded.mode,
			messages = excluded.messages
	`
	_, err = s.db.Exec(query, conv.ID, conv.Title, conv.CreatedAt, conv.Mode, messagesJSON)
	return err
}

func (s *Store) Load(id string) (conversation.Conversation, error) {
	var conv conversation.Conversation
	var messagesJSON sql.NullString
	var createdAt sql.NullTime
	var title sql.NullString
	var mode sql.NullString
	query := `SELECT id, title, created_at, mode, messages FROM conversations WHERE id = ?`
	err := s.db.QueryRow(query, id).Scan(&conv.ID, &title, &createdAt, &mode, &messagesJSON)
	if err != nil {
		return conversation.Conversation{}, err
	}
	conv.Title = title.String
	conv.CreatedAt = createdAt.Time
	conv.Mode = mode.String
	if messagesJSON.Valid && messagesJSON.String != "" {
		if err := json.Unmarshal([]byte(messagesJSON.String), &conv.Messages); err != nil {
			return conversation.Conversation{}, err
		}
	}
	return conv, nil
}

func (s *Store) List() ([]conversation.Conversation, error) {
	query := `SELECT id, title, created_at, mode, messages FROM conversations ORDER BY created_at DESC`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var convs []conversation.Conversation
	for rows.Next() {
		var conv conversation.Conversation
		var messagesJSON sql.NullString
		var createdAt sql.NullTime
		var title sql.NullString
		var mode sql.NullString
		if err := rows.Scan(&conv.ID, &title, &createdAt, &mode, &messagesJSON); err != nil {
			return nil, err
		}
		conv.Title = title.String
		conv.CreatedAt = createdAt.Time
		conv.Mode = mode.String
		if messagesJSON.Valid && messagesJSON.String != "" {
			if err := json.Unmarshal([]byte(messagesJSON.String), &conv.Messages); err != nil {
				// ignore error
			}
		}
		convs = append(convs, conv)
	}
	return convs, nil
}

// ListMeta returns conversations with only metadata (no messages) for fast startup.
func (s *Store) ListMeta() ([]conversation.Conversation, error) {
	query := `SELECT id, title, created_at, mode FROM conversations ORDER BY created_at DESC`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var convs []conversation.Conversation
	for rows.Next() {
		var conv conversation.Conversation
		var createdAt sql.NullTime
		var mode sql.NullString
		var title sql.NullString
		if err := rows.Scan(&conv.ID, &title, &createdAt, &mode); err != nil {
			return nil, err
		}
		conv.Title = title.String
		conv.CreatedAt = createdAt.Time
		conv.Mode = mode.String
		convs = append(convs, conv)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return convs, nil
}

func (s *Store) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM conversations WHERE id = ?`, id)
	return err
}

// Config holds user preferences persisted across sessions.
type Skill struct {
	Mode   string `json:"mode"`
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
}

type Config struct {
	Provider string       `json:"provider"`
	Model    string       `json:"model"`
	Skills   []Skill      `json:"skills"`
	Fonts    SectionFonts `json:"fonts"`
}

type FontStyle struct {
	Face string  `json:"face"`
	Size float32 `json:"size"`
}

type SectionFonts struct {
	Global   FontStyle `json:"global"`
	Sidebar  FontStyle `json:"sidebar"`
	Header   FontStyle `json:"header"`
	Messages FontStyle `json:"messages"`
	Input    FontStyle `json:"input"`
}

func (s *Store) SaveConfig(cfg Config) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO kv_store (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, "config", string(data))
	return err
}

func (s *Store) LoadConfig() (Config, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM kv_store WHERE key = ?`, "config").Scan(&value)
	if err != nil {
		if err == sql.ErrNoRows {
			return Config{}, nil
		}
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal([]byte(value), &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

const maxHistoryEntries = 500

func (s *Store) SaveHistory(entries []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`DELETE FROM history`)
	if err != nil {
		return err
	}

	if len(entries) > maxHistoryEntries {
		entries = entries[len(entries)-maxHistoryEntries:]
	}

	for _, entry := range entries {
		_, err = tx.Exec(`INSERT INTO history (entry) VALUES (?)`, entry)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) LoadHistory() []string {
	rows, err := s.db.Query(`SELECT entry FROM history ORDER BY id ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var entries []string
	for rows.Next() {
		var entry string
		if err := rows.Scan(&entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}
