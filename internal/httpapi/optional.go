package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/danielgtaylor/huma/v2"
)

// Optional is a three-state PATCH field. A JSON object body either carries a key or omits it, and a
// present key may be null; the plain pointer decode collapses an omitted key and an explicit null to
// the same nil, which loses the distinction a PATCH needs — leave the field alone versus clear it.
// UnmarshalJSON runs only for a key that is present, so Set separates omitted from present and Null
// separates an explicit null from a value.
type Optional[T any] struct {
	Value T
	Set   bool
	Null  bool
}

func (o *Optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.Null = true
		return nil
	}
	if err := json.Unmarshal(b, &o.Value); err != nil {
		return fmt.Errorf("optional: %w", err)
	}
	return nil
}

// Schema shapes the field like its underlying T but nullable, so both a value and an explicit null
// validate. allowRef is false so the returned schema is a fresh, mutable one rather than a shared
// registry entry. Field-level constraint tags (minimum, maxLength, …) are layered on afterwards by
// huma, so they keep working.
func (o Optional[T]) Schema(r huma.Registry) *huma.Schema {
	s := r.Schema(reflect.TypeOf(o.Value), false, "")
	s.Nullable = true
	return s
}

// split returns the pointer form the service layer wants: a value pointer, or a clear flag. An
// omitted field is (nil, false); an explicit null is (nil, true); a value is (&value, false).
func (o Optional[T]) split() (*T, bool) {
	switch {
	case !o.Set:
		return nil, false
	case o.Null:
		return nil, true
	default:
		v := o.Value
		return &v, false
	}
}
