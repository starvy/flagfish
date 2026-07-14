//go:build ignore

// Command oas31to30 rewrites the repo's OpenAPI 3.1 document into the 3.0 dialect
// oapi-codegen understands, and prints it to stdout.
//
// Huma emits 3.1, where a nullable field is spelled `type: [T, "null"]`. oapi-codegen
// does not yet support 3.1 and chokes on that multi-type. The only shape that actually
// trips it here is the nullable one, so the transform is narrow: collapse
//
//	type:
//	  - T
//	  - "null"
//
// to `type: T` + `nullable: true` (3.0's spelling, which keeps the generated field a
// pointer so a JSON null still decodes), and drop the version down to 3.0.3. Stdlib
// only, so it adds nothing to go.mod. Run via `task e2e-gen`.
package main

import (
	"fmt"
	"os"
	"regexp"
)

// nullableBlock matches a two-member type union whose second member is "null", capturing
// the key indentation and the non-null base type.
var nullableBlock = regexp.MustCompile(`(?m)^([ \t]*)type:[ \t]*\n[ \t]*-[ \t]*"?([A-Za-z]+)"?[ \t]*\n[ \t]*-[ \t]*"null"[ \t]*$`)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: oas31to30 <openapi.yaml>")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "oas31to30:", err)
		os.Exit(1)
	}

	out := nullableBlock.ReplaceAll(raw, []byte("${1}type: ${2}\n${1}nullable: true"))
	out = regexp.MustCompile(`(?m)^openapi:.*$`).ReplaceAll(out, []byte("openapi: 3.0.3"))

	if _, err := os.Stdout.Write(out); err != nil {
		fmt.Fprintln(os.Stderr, "oas31to30:", err)
		os.Exit(1)
	}
}
