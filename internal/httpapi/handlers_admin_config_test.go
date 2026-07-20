package httpapi

import (
	"reflect"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/config"
)

// The admin config GET must never carry a secret's value. The registry is the
// authority on which keys are secret, so the output struct is held against it:
// no field may be named after a secret key, and every settable secret must
// surface as a presence boolean instead.
func TestAdminConfigOutputDisclosesNoSecret(t *testing.T) {
	body := reflect.TypeOf(adminConfigOutput{}.Body)
	fields := map[string]reflect.StructField{}
	for i := range body.NumField() {
		f := body.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			t.Fatalf("output field %s has no json name; the disclosure check cannot see it", f.Name)
		}
		fields[name] = f
	}

	for _, key := range config.Keys() {
		if !config.Secret(key) {
			continue
		}
		if _, ok := fields[key]; ok {
			t.Errorf("output has a field named after secret config key %q — secrets are set-only, never echoed", key)
		}
		if !config.Modelled(key) {
			continue
		}
		set, ok := fields[key+"_set"]
		if !ok {
			t.Errorf("settable secret %q has no %s_set presence boolean — the operator cannot tell whether it is configured", key, key)
			continue
		}
		if set.Type.Kind() != reflect.Bool {
			t.Errorf("%s_set is %s, want bool: anything richer than presence is disclosure", key, set.Type)
		}
	}
}
