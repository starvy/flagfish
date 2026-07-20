package adminops

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/page"
)

// PageInput is a whole page as the create path supplies it.
type PageInput struct {
	Route        string
	Title        string
	Content      string
	Format       string
	Draft        bool
	AuthRequired bool
}

// PagePatch is a partial page: a nil field is left untouched. It mirrors the COALESCE update, so the
// same absence that keeps a column keeps its validation from running.
type PagePatch struct {
	Route        *string
	Title        *string
	Content      *string
	Format       *string
	Draft        *bool
	AuthRequired *bool
}

// CreatePage inserts a page. The slug's uniqueness is the table's (pages_route_key), so there is no
// pre-check: a duplicate is the constraint's 409, not a race between a SELECT and an INSERT.
func (s *Service) CreatePage(ctx context.Context, actor audit.Actor, in PageInput) (db.Page, error) {
	c, err := page.Validate(in.Route, in.Title, in.Content, in.Format)
	if err != nil {
		return db.Page{}, invalidf("%s", err.Error())
	}
	var out db.Page
	err = s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var createErr error
		out, createErr = q.AdminCreatePage(ctx, db.AdminCreatePageParams{
			Route: c.Route, Title: c.Title, Content: c.Body, Format: c.Format,
			Draft: in.Draft, AuthRequired: in.AuthRequired,
		})
		if isUniqueViolation(createErr) {
			return fmt.Errorf("%w: %q", ErrPageRouteTaken, c.Route)
		} else if createErr != nil {
			return fmt.Errorf("adminops: create page %q: %w", c.Route, createErr)
		}
		return nil
	})
	return out, err
}

// UpdatePage applies a partial change. Each present field is validated with the same rule the create
// path uses; an absent field is not touched. Renaming a page onto another's route is the same 409 a
// duplicate create is.
func (s *Service) UpdatePage(ctx context.Context, actor audit.Actor, id int64, patch PagePatch) (db.Page, error) {
	params := db.AdminUpdatePageParams{ID: id, Draft: patch.Draft, AuthRequired: patch.AuthRequired}
	if patch.Route != nil {
		r, err := page.ValidateRoute(*patch.Route)
		if err != nil {
			return db.Page{}, invalidf("%s", err.Error())
		}
		params.Route = &r
	}
	if patch.Title != nil {
		t, err := page.ValidateTitle(*patch.Title)
		if err != nil {
			return db.Page{}, invalidf("%s", err.Error())
		}
		params.Title = &t
	}
	if patch.Content != nil {
		if _, err := page.ValidateBody(*patch.Content); err != nil {
			return db.Page{}, invalidf("%s", err.Error())
		}
		params.Content = patch.Content
	}
	if patch.Format != nil {
		f, err := page.ValidateFormat(*patch.Format)
		if err != nil {
			return db.Page{}, invalidf("%s", err.Error())
		}
		params.Format = &f
	}

	var out db.Page
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var updateErr error
		out, updateErr = q.AdminUpdatePage(ctx, params)
		switch {
		case errors.Is(updateErr, pgx.ErrNoRows):
			return fmt.Errorf("%w: id=%d", ErrPageNotFound, id)
		case isUniqueViolation(updateErr):
			return fmt.Errorf("%w: id=%d", ErrPageRouteTaken, id)
		case updateErr != nil:
			return fmt.Errorf("adminops: update page %d: %w", id, updateErr)
		}
		return nil
	})
	return out, err
}

// DeletePage removes a page. The delete is audited; zero rows means it was already gone.
func (s *Service) DeletePage(ctx context.Context, actor audit.Actor, id int64) error {
	return s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		n, err := q.AdminDeletePage(ctx, id)
		if err != nil {
			return fmt.Errorf("adminops: delete page %d: %w", id, err)
		}
		if n == 0 {
			return fmt.Errorf("%w: id=%d", ErrPageNotFound, id)
		}
		return nil
	})
}

// ListPages returns every page, draft or not, for the admin index. A plain read, so it runs outside
// the audited transaction the mutations use.
func (s *Service) ListPages(ctx context.Context) ([]db.AdminListPagesRow, error) {
	rows, err := s.q.AdminListPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("adminops: list pages: %w", err)
	}
	return rows, nil
}

// GetPage returns one page with its full body, for the admin editor.
func (s *Service) GetPage(ctx context.Context, id int64) (db.Page, error) {
	p, err := s.q.AdminGetPage(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Page{}, fmt.Errorf("%w: id=%d", ErrPageNotFound, id)
	} else if err != nil {
		return db.Page{}, fmt.Errorf("adminops: get page %d: %w", id, err)
	}
	return p, nil
}

// isUniqueViolation reports whether err is a Postgres unique-constraint violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
