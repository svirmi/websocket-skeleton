package storage

import (
    "database/sql"
    _ "github.com/mattn/go-sqlite3"
    "sync"
)

type SQLiteStore struct {
    db *sql.DB
    mu sync.Mutex
}

func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
    db, err := sql.Open("sqlite3", dbPath)
    if err != nil {
        return nil, err
    }

    // Enable WAL mode for better concurrent access
    if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
        return nil, err
    }

    return &SQLiteStore{
        db: db,
    }, nil
}

func (s *SQLiteStore) SaveData(data []byte) error {
    s.mu.Lock()
    defer s.mu.Unlock()

    _, err := s.db.Exec(`
        INSERT INTO raw_data (data, timestamp) 
        VALUES (?, CURRENT_TIMESTAMP)
    `, data)
    
    return err
}

func (s *SQLiteStore) Close() error {
    return s.db.Close()
}