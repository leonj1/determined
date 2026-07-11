package models

// DocumentURL identifies a remote knowledge document.
type DocumentURL string

// DestinationPath identifies a local knowledge document destination.
type DestinationPath string

// DocumentContent is the complete content of a knowledge document.
type DocumentContent []byte

// InitializationDocument maps one remote document to its local destination.
type InitializationDocument struct {
	Source      DocumentURL
	Destination DestinationPath
}

// InitializationConfig contains the documents installed by -init.
type InitializationConfig struct {
	Documents []InitializationDocument
}
