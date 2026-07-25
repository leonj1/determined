package clients

import (
	"database/sql"
	"fmt"

	"determined/src/models"

	_ "modernc.org/sqlite" // pure-Go sqlite driver registration
)

// SQLiteLogStore persists every streamed status-page output line for the whole
// run, keyed by its monotonic sequence, so the page can fetch a window of lines
// before any point in time long after the in-memory ring has dropped them. It
// implements services.LogHistory.
type SQLiteLogStore struct {
	db *sql.DB
}

// NewSQLiteLogStore opens (creating if needed) the database at path and
// prepares the lines table. The caller owns the file's lifecycle and must call
// Close when the run ends.
func NewSQLiteLogStore(path string) (*SQLiteLogStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("could not open log history database: %w", err)
	}
	// One writer, sequential appends: a single connection avoids lock churn.
	db.SetMaxOpenConns(1)
	schema := `
		PRAGMA journal_mode = WAL;
		CREATE TABLE IF NOT EXISTS lines (
			seq    INTEGER PRIMARY KEY,
			stream TEXT    NOT NULL,
			entry  INTEGER NOT NULL,
			text   TEXT    NOT NULL
		);`
	if _, err := db.Exec(schema); err != nil {
		db.Close() //nolint:errcheck
		return nil, fmt.Errorf("could not prepare log history schema: %w", err)
	}
	return &SQLiteLogStore{db: db}, nil
}

// Record persists one batch of lines atomically.
func (s *SQLiteLogStore) Record(lines []models.LogLine) error {
	if len(lines) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("could not begin log history write: %w", err)
	}
	for _, line := range lines {
		_, err := tx.Exec(
			"INSERT INTO lines (seq, stream, entry, text) VALUES (?, ?, ?, ?)",
			uint64(line.Sequence), string(line.Stream), int(line.Entry), line.Text,
		)
		if err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("could not persist log line %d: %w", line.Sequence, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("could not commit log history write: %w", err)
	}
	return nil
}

// Before returns up to limit lines with sequences strictly below before, in
// ascending order, plus the store's latest sequence so callers can tell where
// the live tail is.
func (s *SQLiteLogStore) Before(before models.LogSequence, limit models.LogBatchSize) (models.LogBatch, error) {
	if limit < 1 {
		limit = 1
	}
	rows, err := s.db.Query(
		`SELECT seq, stream, entry, text FROM
			(SELECT seq, stream, entry, text FROM lines WHERE seq < ? ORDER BY seq DESC LIMIT ?)
		ORDER BY seq ASC`,
		uint64(before), int(limit),
	)
	if err != nil {
		return models.LogBatch{}, fmt.Errorf("could not read log history: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	batch := models.LogBatch{Lines: []models.LogLine{}}
	for rows.Next() {
		line, err := scanLogLine(rows)
		if err != nil {
			return models.LogBatch{}, err
		}
		batch.Lines = append(batch.Lines, line)
		batch.Next = line.Sequence
	}
	if err := rows.Err(); err != nil {
		return models.LogBatch{}, fmt.Errorf("could not read log history: %w", err)
	}
	return s.withLatest(batch)
}

func scanLogLine(rows *sql.Rows) (models.LogLine, error) {
	var seq uint64
	var stream string
	var entry int
	var text string
	if err := rows.Scan(&seq, &stream, &entry, &text); err != nil {
		return models.LogLine{}, fmt.Errorf("could not decode log history row: %w", err)
	}
	return models.LogLine{
		Sequence: models.LogSequence(seq),
		Stream:   models.LogStream(stream),
		Entry:    models.LogEntryIndex(entry),
		Text:     text,
	}, nil
}

func (s *SQLiteLogStore) withLatest(batch models.LogBatch) (models.LogBatch, error) {
	var latest uint64
	err := s.db.QueryRow("SELECT COALESCE(MAX(seq), 0) FROM lines").Scan(&latest)
	if err != nil {
		return models.LogBatch{}, fmt.Errorf("could not read log history extent: %w", err)
	}
	batch.Latest = models.LogSequence(latest)
	return batch, nil
}

// Close releases the database.
func (s *SQLiteLogStore) Close() error {
	return s.db.Close()
}
