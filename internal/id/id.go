package id

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

type Generator interface {
	New(time.Time) (string, error)
}

type CryptoGenerator struct{}

func (CryptoGenerator) New(now time.Time) (string, error) {
	value, err := ulid.New(ulid.Timestamp(now.UTC()), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate ULID: %w", err)
	}
	return "src_" + value.String(), nil
}
