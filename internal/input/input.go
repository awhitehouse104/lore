package input

import (
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

var (
	ErrTooLarge    = errors.New("capture input is too large")
	ErrInvalidUTF8 = errors.New("capture input is not valid UTF-8")
)

func ReadBounded(reader io.Reader, maximum int64) ([]byte, error) {
	if maximum <= 0 {
		return nil, fmt.Errorf("maximum input size must be positive")
	}
	limited := io.LimitReader(reader, maximum+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read capture input: %w", err)
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("%w: configured maximum is %d bytes", ErrTooLarge, maximum)
	}
	if !utf8.Valid(data) {
		return nil, ErrInvalidUTF8
	}
	return data, nil
}
