package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
)

type Keys struct{ reader, writer [sha256.Size]byte }

func NewKeys(reader, writer string) (Keys, error) {
	for _, key := range []string{reader, writer} {
		if key == "" || strings.IndexFunc(key, func(c rune) bool { return c < 33 || c > 126 || c == ',' }) >= 0 {
			return Keys{}, errors.New("configure STOCKROOM_READ_KEY and STOCKROOM_WRITE_KEY as nonempty API tokens")
		}
	}
	if reader == writer {
		return Keys{}, errors.New("reader and writer keys must differ")
	}
	return Keys{sha256.Sum256([]byte(reader)), sha256.Sum256([]byte(writer))}, nil
}

func (k Keys) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		var token string
		if len(values) == 1 {
			scheme, value, ok := strings.Cut(values[0], " ")
			if ok && strings.EqualFold(scheme, "Bearer") {
				token = value
			}
		}
		hash := sha256.Sum256([]byte(token))
		reader := subtle.ConstantTimeCompare(hash[:], k.reader[:])
		writer := subtle.ConstantTimeCompare(hash[:], k.writer[:])
		if token == "" || (reader|writer) == 0 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="stockroom"`)
			fail(w, http.StatusUnauthorized, "valid bearer key required")
			return
		}
		if writer == 0 && r.Method != http.MethodGet && r.Method != http.MethodHead {
			fail(w, http.StatusForbidden, "write permission required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
