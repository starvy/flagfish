// Package notify is the notifications bus: a broadcaster that holds one dedicated Postgres
// connection LISTENing for new notifications and fans each one out to the connected SSE clients,
// and the service that publishes and reads them.
//
// The delivery contract is broadcast-only. Every notification goes to every client; the table
// carries no user_id/team_id because there is nothing to target. Publishing writes the row and
// signals the listeners in one transaction, so persistence and delivery cannot disagree — a row
// that survives a commit is a row the fleet was told to re-read, and a rolled-back write wakes
// nobody.
package notify

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
)

// channel is the LISTEN/NOTIFY channel. The payload is the notification id and nothing more: the
// 8 kB NOTIFY limit is no place to ship content, and the pump reloads the committed row anyway.
const channel = "notifications"

// ErrNotFound reports a notification id with no row — the pump uses it to skip a notification that
// was deleted between the signal and the reload.
var ErrNotFound = errors.New("notify: notification not found")

// Notification is one broadcast message.
type Notification struct {
	ID      int64
	Title   string
	Content string
	Date    time.Time
}

// New is a notification to publish.
type New struct {
	Title   string
	Content string
}

func fromRow(n db.Notification) Notification {
	return Notification{ID: n.ID, Title: n.Title, Content: n.Content, Date: n.Date.Time}
}

// Service is the publish and read side. It owns no connection of its own beyond the pool; the
// dedicated LISTEN connection belongs to the Broadcaster.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool, q: db.New(pool)} }

// Publish writes the notification and signals every replica in the same transaction. The audit
// trigger on the table attributes the row to the acting admin, so the notification and its audit
// entry commit or roll back together.
func (s *Service) Publish(ctx context.Context, actor audit.Actor, n New) (Notification, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Notification{}, fmt.Errorf("notify: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err = audit.Stamp(ctx, tx, actor); err != nil {
		return Notification{}, fmt.Errorf("notify: %w", err)
	}

	row, err := s.q.WithTx(tx).InsertNotification(ctx, db.InsertNotificationParams{
		Title: n.Title, Content: n.Content,
	})
	if err != nil {
		return Notification{}, fmt.Errorf("notify: insert: %w", err)
	}

	// The NOTIFY rides inside the transaction: Postgres holds the signal until commit, so a
	// listener never learns of a row that a later rollback erased.
	if _, err = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, channel, strconv.FormatInt(row.ID, 10)); err != nil {
		return Notification{}, fmt.Errorf("notify: signal: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return Notification{}, fmt.Errorf("notify: commit: %w", err)
	}
	return fromRow(row), nil
}

// PublishSystem publishes a notification with no acting admin — the platform itself is the author.
// It deliberately does not stamp an actor, so the audit trigger records actor_id NULL, which the
// audit_log schema defines as "system". It is how a background job (e.g. a pool-exhaustion alert)
// raises an operator-facing notice that no human triggered.
func (s *Service) PublishSystem(ctx context.Context, title, content string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("notify: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := s.q.WithTx(tx).InsertNotification(ctx, db.InsertNotificationParams{Title: title, Content: content})
	if err != nil {
		return fmt.Errorf("notify: insert: %w", err)
	}
	// The signal rides inside the transaction, so a listener never learns of a row a later rollback
	// would erase.
	if _, err = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, channel, strconv.FormatInt(row.ID, 10)); err != nil {
		return fmt.Errorf("notify: signal: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("notify: commit: %w", err)
	}
	return nil
}

// Get loads one notification by id. The pump calls it to turn a signalled id into the row it fans
// out.
func (s *Service) Get(ctx context.Context, id int64) (Notification, error) {
	row, err := s.q.GetNotification(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Notification{}, ErrNotFound
		}
		return Notification{}, fmt.Errorf("notify: get %d: %w", id, err)
	}
	return fromRow(row), nil
}

// Recent returns up to limit of the newest notifications, newest first, for replay on connect.
func (s *Service) Recent(ctx context.Context, limit int32) ([]Notification, error) {
	rows, err := s.q.RecentNotifications(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("notify: recent: %w", err)
	}
	out := make([]Notification, len(rows))
	for i, r := range rows {
		out[i] = fromRow(r)
	}
	return out, nil
}

// Page is one page of the notifications list.
type Page struct {
	Items []Notification
	Total int64
}

// List returns a page of notifications for clients that poll instead of streaming. page is
// 1-based; the handler bounds both arguments, so an over-large offset merely yields an empty page.
func (s *Service) List(ctx context.Context, page, perPage int) (Page, error) {
	rows, err := s.q.ListNotifications(ctx, db.ListNotificationsParams{
		Off: int32((page - 1) * perPage), //nolint:gosec // page is bounded by the handler; an over-large offset just returns an empty page
		Lim: int32(perPage),              //nolint:gosec // Huma caps per_page at 100
	})
	if err != nil {
		return Page{}, fmt.Errorf("notify: list: %w", err)
	}
	out := Page{Items: make([]Notification, len(rows))}
	for i, r := range rows {
		out.Items[i] = Notification{ID: r.ID, Title: r.Title, Content: r.Content, Date: r.Date.Time}
		out.Total = r.Total
	}
	return out, nil
}
