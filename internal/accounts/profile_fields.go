package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/field"
)

// FieldAnswer is one custom-field answer as it arrived from a caller: the field it targets and the
// raw JSON value. The value is validated against the field's type before it is written; a nil or
// JSON-null value means "no answer" (a clear, on the edit path).
type FieldAnswer struct {
	FieldID int64
	Value   json.RawMessage
}

// FieldAnswerError is a custom-field answer this layer refused. Reason is written for the end user
// and safe to put on the wire — it never carries database error text. The handler maps it to 422,
// which is the whole point on the register path: a required field left blank must fail loudly at
// sign-up, not pass and then strand the account behind the profile-complete login gate.
type FieldAnswerError struct {
	FieldID int64
	Name    string
	Reason  string
}

func (e *FieldAnswerError) Error() string {
	if e.Name != "" {
		return fmt.Sprintf("accounts: field %q (%d): %s", e.Name, e.FieldID, e.Reason)
	}
	return fmt.Sprintf("accounts: field %d: %s", e.FieldID, e.Reason)
}

// UserField is one custom field with this caller's current answer, for the /me editor. Value is nil
// when the field is unanswered.
type UserField struct {
	ID          int64
	Name        string
	FieldType   string
	Description *string
	Required    bool
	Public      bool
	Editable    bool
	Position    int32
	Value       json.RawMessage
}

// PublicFieldAnswer is one answered, public field on a user's public profile. Only public,
// answered fields are ever represented here — a non-public answer is not exposed.
type PublicFieldAnswer struct {
	ID        int64
	Name      string
	FieldType string
	Value     json.RawMessage
}

// answerFault turns a domain validation error into the user-facing reason for a field.
func answerFault(err error) string {
	switch {
	case errors.Is(err, field.ErrTypeMismatch):
		return "answer does not match the field type"
	case errors.Is(err, field.ErrTextTooLong):
		return "answer is too long"
	default:
		return "answer is invalid"
	}
}

// writeRegistrationAnswers validates the submitted answers against the user-applicable field
// definitions and writes each present answer, inside the caller's transaction. Every required field
// must be answered, an answer to an unknown field is refused, and a type mismatch is refused —
// loud, never coerced. Because it runs in the registration transaction, the account and its
// required answers commit together: a required field cannot be left unanswered by a half-finished
// sign-up, which is exactly the state that would brick the profile-complete login gate.
func writeRegistrationAnswers(ctx context.Context, q *db.Queries, userID int64, answers []FieldAnswer) error {
	defs, err := q.ListUserFieldDefs(ctx)
	if err != nil {
		return fmt.Errorf("accounts: load field defs: %w", err)
	}
	byID := make(map[int64]db.ListUserFieldDefsRow, len(defs))
	for _, d := range defs {
		byID[d.ID] = d
	}

	answered := make(map[int64]bool, len(answers))
	for _, a := range answers {
		d, ok := byID[a.FieldID]
		if !ok {
			return &FieldAnswerError{FieldID: a.FieldID, Reason: "no such registration field"}
		}
		value, present, normErr := field.Normalize(d.FieldType, a.Value)
		if normErr != nil {
			return &FieldAnswerError{FieldID: d.ID, Name: d.Name, Reason: answerFault(normErr)}
		}
		if !present {
			continue // an absent optional answer writes nothing; a required one is caught below
		}
		if err := q.UpsertUserFieldEntry(ctx, db.UpsertUserFieldEntryParams{
			FieldID: d.ID, UserID: &userID, Value: value,
		}); err != nil {
			return fmt.Errorf("accounts: write field %d answer: %w", d.ID, err)
		}
		answered[d.ID] = true
	}

	for _, d := range defs {
		if d.Required && !answered[d.ID] {
			return &FieldAnswerError{FieldID: d.ID, Name: d.Name, Reason: "this field is required"}
		}
	}
	return nil
}

// RegistrationFields returns the user-applicable field definitions, no answers — what the
// registration form renders before an account exists. Value is always nil here.
func (s *Service) RegistrationFields(ctx context.Context) ([]UserField, error) {
	rows, err := s.q.ListUserFieldDefs(ctx)
	if err != nil {
		return nil, fmt.Errorf("accounts: registration fields: %w", err)
	}
	out := make([]UserField, len(rows))
	for i, r := range rows {
		out[i] = UserField{
			ID: r.ID, Name: r.Name, FieldType: r.FieldType, Description: r.Description,
			Required: r.Required, Public: r.Public, Editable: r.Editable, Position: r.Position,
		}
	}
	return out, nil
}

// UserFields returns every user-applicable field with this caller's current answer, for the /me
// settings editor — both the fields still owed and the ones already filled in.
func (s *Service) UserFields(ctx context.Context, userID int64) ([]UserField, error) {
	rows, err := s.q.ListUserFieldsWithAnswers(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("accounts: list user fields: %w", err)
	}
	out := make([]UserField, len(rows))
	for i, r := range rows {
		out[i] = UserField{
			ID: r.ID, Name: r.Name, FieldType: r.FieldType, Description: r.Description,
			Required: r.Required, Public: r.Public, Editable: r.Editable, Position: r.Position,
			Value: r.Value,
		}
	}
	return out, nil
}

// PublicFields returns only the public, answered fields of a user — the projection a public profile
// view is allowed to show. A non-public answer never leaves this method.
func (s *Service) PublicFields(ctx context.Context, userID int64) ([]PublicFieldAnswer, error) {
	rows, err := s.q.ListPublicUserFieldAnswers(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("accounts: list public fields: %w", err)
	}
	out := make([]PublicFieldAnswer, len(rows))
	for i, r := range rows {
		out[i] = PublicFieldAnswer{ID: r.ID, Name: r.Name, FieldType: r.FieldType, Value: r.Value}
	}
	return out, nil
}

// AnswerUserFields writes self-service answers from the /me editor. A field is writable when it is
// editable, or when it is required and not yet answered — the second clause is the remedy that
// keeps a required field created after sign-up always answerable, so an account can never be
// permanently held behind the profile-complete gate. A submitted answer to a non-writable field is
// refused, and a required field cannot be cleared.
func (s *Service) AnswerUserFields(ctx context.Context, userID int64, answers []FieldAnswer) ([]UserField, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("accounts: answer fields: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	rows, err := q.ListUserFieldsWithAnswers(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("accounts: answer fields: load: %w", err)
	}
	byID := make(map[int64]db.ListUserFieldsWithAnswersRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}

	for _, a := range answers {
		r, ok := byID[a.FieldID]
		if !ok {
			return nil, &FieldAnswerError{FieldID: a.FieldID, Reason: "no such field"}
		}
		alreadyAnswered := field.IsAnswered(r.Value)
		if !r.Editable && (!r.Required || alreadyAnswered) {
			return nil, &FieldAnswerError{FieldID: r.ID, Name: r.Name, Reason: "this field cannot be edited"}
		}
		value, present, normErr := field.Normalize(r.FieldType, a.Value)
		if normErr != nil {
			return nil, &FieldAnswerError{FieldID: r.ID, Name: r.Name, Reason: answerFault(normErr)}
		}
		if present {
			if err := q.UpsertUserFieldEntry(ctx, db.UpsertUserFieldEntryParams{
				FieldID: r.ID, UserID: &userID, Value: value,
			}); err != nil {
				return nil, fmt.Errorf("accounts: write field %d answer: %w", r.ID, err)
			}
			continue
		}
		// An empty answer clears the field — but a required field must stay answered.
		if r.Required {
			return nil, &FieldAnswerError{FieldID: r.ID, Name: r.Name, Reason: "this field is required and cannot be cleared"}
		}
		if err := q.DeleteUserFieldEntry(ctx, db.DeleteUserFieldEntryParams{FieldID: r.ID, UserID: &userID}); err != nil {
			return nil, fmt.Errorf("accounts: clear field %d answer: %w", r.ID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("accounts: answer fields: commit: %w", err)
	}
	return s.UserFields(ctx, userID)
}
