package services

import (
	"context"
	"fmt"

	"determined/src/models"
)

// DocumentSource retrieves knowledge documents from the outside world.
type DocumentSource interface {
	Fetch(context.Context, models.DocumentURL) (models.DocumentContent, error)
}

// DocumentStore replaces local knowledge documents.
type DocumentStore interface {
	Replace(models.DestinationPath, models.DocumentContent) error
}

// InitializationService installs the personal knowledge documents.
type InitializationService struct {
	source DocumentSource
	store  DocumentStore
	cfg    models.InitializationConfig
}

// NewInitializationService wires an InitializationService from its dependencies.
func NewInitializationService(
	source DocumentSource,
	store DocumentStore,
	cfg models.InitializationConfig,
) *InitializationService {
	return &InitializationService{source: source, store: store, cfg: cfg}
}

// Run fetches every document before replacing any local destination.
func (s *InitializationService) Run(ctx context.Context) error {
	contents := make([]models.DocumentContent, 0, len(s.cfg.Documents))
	for _, document := range s.cfg.Documents {
		content, err := s.source.Fetch(ctx, document.Source)
		if err != nil {
			return fmt.Errorf("fetch %s: %w", document.Source, err)
		}
		contents = append(contents, content)
	}
	for index, document := range s.cfg.Documents {
		if err := s.store.Replace(document.Destination, contents[index]); err != nil {
			return fmt.Errorf("write %s: %w", document.Destination, err)
		}
	}
	return nil
}
