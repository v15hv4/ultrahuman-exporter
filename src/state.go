package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

const stateKeyLastSuccess = "last_success_epoch"

func openStateDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS state (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func loadLastSuccessEpoch(db *sql.DB) (int64, error) {
	var raw string
	err := db.QueryRow(`SELECT value FROM state WHERE key = ?`, stateKeyLastSuccess).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		initial := time.Now().Add(-24 * time.Hour).Unix()
		return initial, saveLastSuccessEpoch(db, initial)
	}
	if err != nil {
		return 0, err
	}

	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid stored %s value %q: %w", stateKeyLastSuccess, raw, err)
	}

	return value, nil
}

func saveLastSuccessEpoch(db *sql.DB, epoch int64) error {
	_, err := db.Exec(
		`INSERT INTO state(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		stateKeyLastSuccess,
		strconv.FormatInt(epoch, 10),
	)
	return err
}
