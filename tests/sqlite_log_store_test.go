package tests

import (
	"path/filepath"
	"testing"

	"determined/src/clients"
	"determined/src/models"
)

// These are contract tests against a real sqlite database file: the store is
// the production adapter behind services.LogHistory, so its boundary behavior
// is proven here rather than through fakes.

func openLogStore(t *testing.T) *clients.SQLiteLogStore {
	t.Helper()
	store, err := clients.NewSQLiteLogStore(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() }) //nolint:errcheck
	return store
}

func recordedLines(from, to int) []models.LogLine {
	lines := make([]models.LogLine, 0, to-from+1)
	for seq := from; seq <= to; seq++ {
		stream := models.LogStreamPlan
		if seq%2 == 0 {
			stream = models.LogStreamExec
		}
		lines = append(lines, models.LogLine{
			Sequence: models.LogSequence(seq), Stream: stream,
			Entry: models.LogEntryIndex(seq / 10), Text: "line\n",
		})
	}
	return lines
}

func TestSQLiteLogStoreReturnsExactWindowBeforeASequence(t *testing.T) {
	store := openLogStore(t)
	if err := store.Record(recordedLines(1, 40)); err != nil {
		t.Fatalf("record: %v", err)
	}

	batch, err := store.Before(21, 10)
	if err != nil {
		t.Fatalf("before: %v", err)
	}
	if len(batch.Lines) != 10 || batch.Latest != 40 || batch.Next != 20 {
		t.Fatalf("batch = %+v, want 10 lines, next 20, latest 40", batch)
	}
	for i, line := range batch.Lines {
		want := models.LogSequence(11 + i)
		if line.Sequence != want {
			t.Fatalf("line %d sequence = %d, want %d (ascending 11..20)", i, line.Sequence, want)
		}
	}
	if batch.Lines[9].Stream != models.LogStreamExec || batch.Lines[9].Entry != 2 {
		t.Fatalf("line = %+v, want stream and entry restored", batch.Lines[9])
	}
}

func TestSQLiteLogStoreClampsAtTheOldestPersistedLine(t *testing.T) {
	store := openLogStore(t)
	if err := store.Record(recordedLines(1, 5)); err != nil {
		t.Fatalf("record: %v", err)
	}

	batch, err := store.Before(4, 10)
	if err != nil {
		t.Fatalf("before: %v", err)
	}
	if len(batch.Lines) != 3 || batch.Lines[0].Sequence != 1 || batch.Lines[2].Sequence != 3 {
		t.Fatalf("batch = %+v, want exactly sequences 1..3", batch)
	}

	empty, err := store.Before(1, 10)
	if err != nil {
		t.Fatalf("before oldest: %v", err)
	}
	if len(empty.Lines) != 0 || empty.Latest != 5 {
		t.Fatalf("batch = %+v, want no lines and latest 5", empty)
	}
}

func TestSQLiteLogStoreRecordIsAtomicAcrossBatches(t *testing.T) {
	store := openLogStore(t)
	if err := store.Record(recordedLines(1, 3)); err != nil {
		t.Fatalf("record: %v", err)
	}
	// A batch replaying an already-persisted sequence violates the primary key
	// and must leave none of its lines behind.
	if err := store.Record(recordedLines(3, 6)); err == nil {
		t.Fatal("recording a duplicate sequence succeeded, want primary-key failure")
	}

	batch, err := store.Before(100, 100)
	if err != nil {
		t.Fatalf("before: %v", err)
	}
	if len(batch.Lines) != 3 || batch.Latest != 3 {
		t.Fatalf("batch = %+v, want only the first intact batch persisted", batch)
	}
}
