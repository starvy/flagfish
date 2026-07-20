package httpapi

import (
	"encoding/json"

	"github.com/danielgtaylor/huma/v2"
)

// jsonValue is an arbitrary JSON scalar on the wire — a custom field answer is a string for a text
// field or a bool for a checkbox, and the type is validated server-side against the field
// definition, not by the transport. It carries the raw bytes through unchanged so the domain
// validator sees exactly what the client sent.
type jsonValue struct {
	Raw json.RawMessage
}

// Schema advertises "any JSON value": an empty schema places no type constraint, which is correct
// here because the permitted shape depends on a field the schema cannot see. huma calls this via
// the SchemaProvider interface, the same seam Optional uses.
func (jsonValue) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{}
}

func (v *jsonValue) UnmarshalJSON(b []byte) error {
	v.Raw = append(v.Raw[:0:0], b...)
	return nil
}

func (v jsonValue) MarshalJSON() ([]byte, error) {
	if len(v.Raw) == 0 {
		return []byte("null"), nil
	}
	return v.Raw, nil
}
