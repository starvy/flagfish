package adminops

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/field"
)

// NewField is the create input for a custom registration field. FieldType and AppliesTo fix how
// every answer is interpreted, so — like a bracket's applies_to — they are set once here and are
// not patchable: changing either would reinterpret answers already on file.
type NewField struct {
	Name        string
	AppliesTo   string
	FieldType   string
	Description *string
	Required    bool
	Public      bool
	Editable    bool
	Position    int32
}

// FieldPatch is a partial update: a nil pointer keeps the current value. Type and owner kind are
// absent on purpose (immutable). ClearDescription drops the help text.
type FieldPatch struct {
	Name             *string
	Description      *string
	ClearDescription bool
	Required         *bool
	Public           *bool
	Editable         *bool
	Position         *int32
}

// ListFields returns every custom field, admin view. A plain read, so it runs outside the audited
// transaction the mutations use.
func (s *Service) ListFields(ctx context.Context) ([]db.Field, error) {
	rows, err := s.q.AdminListFields(ctx)
	if err != nil {
		return nil, fmt.Errorf("adminops: list fields: %w", err)
	}
	return rows, nil
}

// CountFieldEntries reports how many answers a field carries, so the delete surface can warn that a
// removal takes N answers with it.
func (s *Service) CountFieldEntries(ctx context.Context, fieldID int64) (int64, error) {
	n, err := s.q.CountFieldEntries(ctx, fieldID)
	if err != nil {
		return 0, fmt.Errorf("adminops: count field %d entries: %w", fieldID, err)
	}
	return n, nil
}

//nolint:gocritic // hugeParam: the create input is a value; a pointer would invite mutation mid-call.
func (s *Service) CreateField(ctx context.Context, actor audit.Actor, in NewField) (db.Field, error) {
	name, err := field.CleanName(in.Name)
	if err != nil {
		return db.Field{}, invalidf("%s", err.Error())
	}
	if !field.ValidAppliesTo(in.AppliesTo) {
		return db.Field{}, invalidf("applies_to must be 'user' or 'team'")
	}
	if !field.ValidType(in.FieldType) {
		return db.Field{}, invalidf("field_type must be 'text' or 'boolean'")
	}
	desc, err := field.CleanDescription(in.Description)
	if err != nil {
		return db.Field{}, invalidf("%s", err.Error())
	}

	var out db.Field
	err = s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var createErr error
		out, createErr = q.AdminCreateField(ctx, db.AdminCreateFieldParams{
			Name: name, AppliesTo: in.AppliesTo, FieldType: in.FieldType, Description: desc,
			Required: in.Required, Public: in.Public, Editable: in.Editable, Position: in.Position,
		})
		if createErr != nil {
			return fmt.Errorf("adminops: create field: %w", createErr)
		}
		return nil
	})
	return out, err
}

//nolint:gocritic // hugeParam: FieldPatch is a small value; a pointer would only add indirection.
func (s *Service) UpdateField(ctx context.Context, actor audit.Actor, fieldID int64, patch FieldPatch) (db.Field, error) {
	var name *string
	if patch.Name != nil {
		cleaned, err := field.CleanName(*patch.Name)
		if err != nil {
			return db.Field{}, invalidf("%s", err.Error())
		}
		name = &cleaned
	}
	desc := patch.Description
	if desc != nil {
		cleaned, err := field.CleanDescription(desc)
		if err != nil {
			return db.Field{}, invalidf("%s", err.Error())
		}
		// A description that trims to empty is a clear, so it does not masquerade as "keep".
		if cleaned == nil {
			desc, patch.ClearDescription = nil, true
		} else {
			desc = cleaned
		}
	}

	var out db.Field
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		var updErr error
		out, updErr = q.AdminUpdateField(ctx, db.AdminUpdateFieldParams{
			FieldID: fieldID, Name: name,
			Description: desc, ClearDescription: patch.ClearDescription,
			Required: patch.Required, Public: patch.Public, Editable: patch.Editable, Position: patch.Position,
		})
		if errors.Is(updErr, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrFieldNotFound, fieldID)
		} else if updErr != nil {
			return fmt.Errorf("adminops: update field %d: %w", fieldID, updErr)
		}
		return nil
	})
	return out, err
}

// DeleteField removes a field and, by the ON DELETE CASCADE FK, its answers. Answers are user data,
// not a gameplay ledger, so a retired question can be taken down without touching anyone's score;
// the audit trigger records the field delete and each removed answer.
func (s *Service) DeleteField(ctx context.Context, actor audit.Actor, fieldID int64) error {
	return s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		n, err := q.AdminDeleteField(ctx, fieldID)
		if err != nil {
			return fmt.Errorf("adminops: delete field %d: %w", fieldID, err)
		}
		if n == 0 {
			return fmt.Errorf("%w: id=%d", ErrFieldNotFound, fieldID)
		}
		return nil
	})
}
