package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/anticheat"
	"github.com/starvy/flagfish/internal/board"
	"github.com/starvy/flagfish/internal/catalog"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/files"
	"github.com/starvy/flagfish/internal/gameplay"
	"github.com/starvy/flagfish/internal/httpapi"
	"github.com/starvy/flagfish/internal/notify"
	"github.com/starvy/flagfish/internal/opsjob"
	"github.com/starvy/flagfish/internal/stats"
)

// OpenAPIYAML renders one of the two API contracts — the admin surface when admin is true, the
// public one otherwise. Every service is constructed without a pool: route registration only records
// operation schemas and no handler runs, so the document reflects the real server without touching a
// database. Registration is conditional on the services being non-nil, so they all have to be here or
// the document would silently lose the routes they own.
func OpenAPIYAML(ctx context.Context, log *slog.Logger, admin bool) ([]byte, error) {
	const mode = account.ModeUsers

	cfg, err := config.New(ctx, defaultConfigStore{}, log)
	if err != nil {
		return nil, fmt.Errorf("app: openapi: %w", err)
	}

	notifySvc := notify.NewService(nil)
	srv := httpapi.New(httpapi.Options{
		Log:         log,
		Config:      cfg,
		Accounts:    accounts.NewService(nil, mode, log),
		Gameplay:    gameplay.New(nil, nil, mode),
		Catalog:     catalog.New(nil),
		Board:       board.New(nil, mode),
		AdminOps:    adminops.New(nil),
		Ops:         opsjob.New(nil, nil, nil, log),
		Anticheat:   anticheat.New(nil),
		Stats:       stats.New(nil),
		Notify:      notifySvc,
		Broadcaster: notify.NewBroadcaster(nil, notifySvc, log),
		Files:       files.New(nil, nil, log),
	})

	api := srv.Public
	if admin {
		api = srv.Admin
	}
	doc, err := api.OpenAPI().YAML()
	if err != nil {
		return nil, fmt.Errorf("app: openapi: %w", err)
	}
	return doc, nil
}

// defaultConfigStore is an empty config table: the document generator needs a Manager to register
// the config routes, but the schemas it records do not depend on a single stored value. A write here
// is a bug in the generator, not an operator error, so it fails rather than pretending to store.
type defaultConfigStore struct{}

func (defaultConfigStore) All(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

func (defaultConfigStore) Mode(context.Context) (account.Mode, bool, error) {
	return account.ModeUsers, false, nil
}

func (defaultConfigStore) Replace(context.Context, map[string]string) error {
	return errors.New("app: openapi: the document generator has no config store to write to")
}
