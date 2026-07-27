package output

import (
	"encoding/json"
	"io"
)

func JSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
