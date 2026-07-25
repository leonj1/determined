package services

import (
	"determined/src/models"
)

// LogHistory persists every streamed output line for the whole run, so the
// status page can page backwards to any point in time even after the live
// ring buffer has overwritten it. The real implementation is
// clients.SQLiteLogStore.
type LogHistory interface {
	Record([]models.LogLine) error
	Before(models.LogSequence, models.LogBatchSize) (models.LogBatch, error)
}
