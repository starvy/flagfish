package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/domain/policy"
)

type createTokenInput struct {
	Body struct {
		Description *string `json:"description,omitempty" maxLength:"255"`
		TTLHours    *int    `json:"ttl_hours,omitempty" minimum:"1" maximum:"8760"`
	}
}

type createTokenOutput struct {
	Body struct {
		ID          int64     `json:"id"`
		Token       string    `json:"token" doc:"The plaintext token. Shown exactly once; store it now, it cannot be retrieved again."`
		Description *string   `json:"description,omitempty"`
		ExpiresAt   time.Time `json:"expires_at"`
	}
}

type tokenListItem struct {
	ID          int64     `json:"id"`
	Description *string   `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type listTokensOutput struct {
	Body struct {
		Tokens []tokenListItem `json:"tokens"`
	}
}

type deleteTokenInput struct {
	ID int64 `path:"id"`
}

type okOutput struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

func (s *Server) registerTokens() {
	Register(s.Public, policy.ClassTokens, huma.Operation{
		OperationID: "create-token", Method: http.MethodPost, Path: "/tokens",
		Summary: "Mint an API token", Tags: []string{"tokens"},
	}, s.createToken)

	Register(s.Public, policy.ClassTokens, huma.Operation{
		OperationID: "list-tokens", Method: http.MethodGet, Path: "/tokens",
		Summary: "List the caller's API tokens", Tags: []string{"tokens"},
	}, s.listTokens)

	Register(s.Public, policy.ClassTokens, huma.Operation{
		OperationID: "delete-token", Method: http.MethodDelete, Path: "/tokens/{id}",
		Summary: "Revoke an API token", Tags: []string{"tokens"},
	}, s.deleteToken)
}

func (s *Server) createToken(ctx context.Context, in *createTokenInput) (*createTokenOutput, error) {
	pr := AuthOf(ctx).Principal

	// Omitted means the service default; a present value is already schema-bounded to 1..8760.
	var ttl time.Duration
	if in.Body.TTLHours != nil {
		ttl = time.Duration(*in.Body.TTLHours) * time.Hour
	}

	tok, err := s.opts.Accounts.CreateToken(ctx, pr.UserID, in.Body.Description, ttl)
	if err != nil {
		s.opts.Log.ErrorContext(ctx, "create token failed", "error", err)
		return nil, huma.Error500InternalServerError("could not create token")
	}

	out := &createTokenOutput{}
	out.Body.ID = tok.ID
	out.Body.Token = tok.Plaintext
	out.Body.Description = tok.Description
	out.Body.ExpiresAt = tok.ExpiresAt
	return out, nil
}

func (s *Server) listTokens(ctx context.Context, _ *struct{}) (*listTokensOutput, error) {
	pr := AuthOf(ctx).Principal
	tokens, err := s.opts.Accounts.ListTokens(ctx, pr.UserID)
	if err != nil {
		s.opts.Log.ErrorContext(ctx, "list tokens failed", "error", err)
		return nil, huma.Error500InternalServerError("could not list tokens")
	}

	out := &listTokensOutput{}
	out.Body.Tokens = make([]tokenListItem, len(tokens))
	for i, t := range tokens {
		out.Body.Tokens[i] = tokenListItem{
			ID:          t.ID,
			Description: t.Description,
			CreatedAt:   t.CreatedAt,
			ExpiresAt:   t.ExpiresAt,
		}
	}
	return out, nil
}

func (s *Server) deleteToken(ctx context.Context, in *deleteTokenInput) (*okOutput, error) {
	pr := AuthOf(ctx).Principal
	err := s.opts.Accounts.DeleteToken(ctx, pr.UserID, in.ID)
	switch {
	case errors.Is(err, accounts.ErrTokenNotFound):
		return nil, huma.Error404NotFound("no such token")
	case err != nil:
		s.opts.Log.ErrorContext(ctx, "delete token failed", "error", err)
		return nil, huma.Error500InternalServerError("could not revoke token")
	}

	out := &okOutput{}
	out.Body.OK = true
	return out, nil
}
