package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MinimumBearerTokenBytes         = 32
	MaximumBearerTokenBytes         = 1024
	MaximumAuthorizationHeaderBytes = 4096
)

type TokenDigest [sha256.Size]byte

type BearerPrincipal struct {
	Principal Principal
	Digest    TokenDigest
}

type BearerAuthenticator struct {
	principals []BearerPrincipal
}

func NewBearerAuthenticator(principals []BearerPrincipal) *BearerAuthenticator {
	cloned := make([]BearerPrincipal, len(principals))
	for index, entry := range principals {
		cloned[index] = entry
		cloned[index].Principal = entry.Principal.Clone()
	}
	return &BearerAuthenticator{principals: cloned}
}

func DecodeBearerToken(text string) ([]byte, error) {
	if text == "" || len(text) > MaximumAuthorizationHeaderBytes || !utf8.ValidString(text) {
		return nil, fmt.Errorf("bearer token encoding is invalid")
	}
	for _, value := range []byte(text) {
		if value <= 0x20 || value == 0x7f {
			return nil, fmt.Errorf("bearer token encoding is invalid")
		}
	}
	var decoded []byte
	var err error
	if len(text)%2 == 0 && isHex(text) {
		decoded, err = hex.DecodeString(text)
	} else {
		decoded, err = base64.RawURLEncoding.Strict().DecodeString(text)
		if err != nil {
			decoded, err = base64.URLEncoding.Strict().DecodeString(text)
		}
	}
	if err != nil || len(decoded) < MinimumBearerTokenBytes || len(decoded) > MaximumBearerTokenBytes {
		return nil, fmt.Errorf("bearer token must be hex or base64url encoding of 32 to %d bytes", MaximumBearerTokenBytes)
	}
	return decoded, nil
}

func DigestBearerToken(decoded []byte) TokenDigest {
	return sha256.Sum256(decoded)
}

func (a *BearerAuthenticator) Authenticate(values []string) (Principal, bool) {
	if a == nil || len(values) != 1 {
		return Principal{}, false
	}
	value := values[0]
	valid := len(value) <= MaximumAuthorizationHeaderBytes &&
		len(value) > len("Bearer ") &&
		strings.EqualFold(value[:len("Bearer")], "Bearer") &&
		value[len("Bearer")] == ' '
	var candidate TokenDigest
	if valid {
		decoded, err := DecodeBearerToken(value[len("Bearer "):])
		if err == nil {
			candidate = DigestBearerToken(decoded)
		} else {
			valid = false
		}
	}
	var matched Principal
	found := 0
	for _, entry := range a.principals {
		equal := subtle.ConstantTimeCompare(candidate[:], entry.Digest[:])
		if equal == 1 && valid {
			matched = entry.Principal.Clone()
			found++
		}
	}
	return matched, found == 1
}

func isHex(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if !((character >= '0' && character <= '9') ||
			(character >= 'a' && character <= 'f') ||
			(character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

type principalContextKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal.Clone())
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	if !ok {
		return Principal{}, false
	}
	return principal.Clone(), true
}
