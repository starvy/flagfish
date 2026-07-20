package award_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/domain/award"
)

func TestValidateManual(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		value      int32
		reason     string
		wantReason string
		wantErr    error
	}{
		{"positive with reason", 100, "make-good for downtime", "make-good for downtime", nil},
		{"negative penalty", -250, "flag-sharing penalty, ticket 42", "flag-sharing penalty, ticket 42", nil},
		{"reason is trimmed", 10, "  padded  ", "padded", nil},
		{"zero is refused", 0, "noise", "", award.ErrZeroValue},
		{"zero beats an empty reason", 0, "", "", award.ErrZeroValue},
		{"empty reason is refused", 50, "", "", award.ErrEmptyReason},
		{"whitespace-only reason is refused", 50, "   \t\n", "", award.ErrEmptyReason},
		{"reason at the cap is accepted", 5, strings.Repeat("x", award.MaxReasonLen), strings.Repeat("x", award.MaxReasonLen), nil},
		{"reason over the cap is refused", 5, strings.Repeat("x", award.MaxReasonLen+1), "", award.ErrReasonTooLong},
		{"multibyte reason counts runes, not bytes", 5, strings.Repeat("é", award.MaxReasonLen), strings.Repeat("é", award.MaxReasonLen), nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := award.ValidateManual(tt.value, tt.reason)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateManual(%d, %q) err = %v, want %v", tt.value, tt.reason, err, tt.wantErr)
			}
			if got != tt.wantReason {
				t.Fatalf("ValidateManual(%d, %q) reason = %q, want %q", tt.value, tt.reason, got, tt.wantReason)
			}
		})
	}
}
