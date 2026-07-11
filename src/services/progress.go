package services

import (
	"fmt"
	"io"
)

// progressMessage is a brief description of the active workflow stage.
type progressMessage string

// writeProgress makes workflow transitions visible with a wall-clock timestamp.
func writeProgress(out io.Writer, clock Clock, message progressMessage) {
	stamp := clock.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(out, "\n==> [%s] %s\n", stamp, message)
}
