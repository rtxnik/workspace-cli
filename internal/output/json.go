package output

import (
	"encoding/json"
	"io"
	"os"
)

// WriteJSON encodes v as 2-space-indented JSON to w, terminated by a
// newline. It is the single display-JSON encoder for the CLI. It never
// exits the process; callers own error handling.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// JSON encodes v as indented JSON to stdout.
func JSON(v any) {
	_ = WriteJSON(os.Stdout, v)
}
