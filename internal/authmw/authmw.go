// Package authmw provides HTTP middleware for bearer token authentication.
package authmw

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// BearerToken returns middleware that validates the Authorization header
// contains a Bearer token matching the expected value. Comparison uses
// constant-time equality to prevent timing side-channel attacks.
func BearerToken(token string) func(http.Handler) http.Handler {
	expected := []byte(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")

			if !strings.HasPrefix(auth, "Bearer ") {
				http.Error(w, `{"error":"missing or malformed authorization header"}`, http.StatusUnauthorized)
				return
			}

			got := []byte(auth[len("Bearer "):])

			if subtle.ConstantTimeCompare(got, expected) != 1 {
				http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
