package app

import (
	"fmt"
	"log/slog"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/board"
	"github.com/starvy/flagfish/internal/catalog"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/files"
	"github.com/starvy/flagfish/internal/gameplay"
	"github.com/starvy/flagfish/internal/httpapi"
	"github.com/starvy/flagfish/internal/notify"
)

// OpenAPIYAML renders the public API contract. The services are constructed without a pool: route
// registration only records operation schemas, and no handler runs, so the document reflects the real
// server without touching a database.
func OpenAPIYAML(log *slog.Logger) ([]byte, error) {
	const mode = account.ModeUsers
	notifySvc := notify.NewService(nil)
	srv := httpapi.New(httpapi.Options{
		Log:         log,
		Accounts:    accounts.NewService(nil, mode, log),
		Gameplay:    gameplay.New(nil, nil, mode),
		Catalog:     catalog.New(nil),
		Board:       board.New(nil, mode),
		Notify:      notifySvc,
		Broadcaster: notify.NewBroadcaster(nil, notifySvc, log),
		Files:       files.New(nil, nil, log),
	})
	doc, err := srv.Public.OpenAPI().YAML()
	if err != nil {
		return nil, fmt.Errorf("app: openapi: %w", err)
	}
	return doc, nil
}
