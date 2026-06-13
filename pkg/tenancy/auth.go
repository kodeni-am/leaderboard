package tenancy

import (
	"context"
	"net/http"
	"strings"
)

type contextKey int

const appContextKey contextKey = 0

// AppFromContext returns the authenticated tenant attached by Authenticate.
func AppFromContext(ctx context.Context) (App, bool) {
	app, ok := ctx.Value(appContextKey).(App)
	return app, ok
}

// extractKey pulls the API key from "Authorization: Bearer <key>" or the
// "X-API-Key" header.
func extractKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if after, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(after)
		}
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

// Authenticate is middleware that resolves the API key to a tenant and stores
// it in the request context. Unauthenticated requests get 401.
func Authenticate(store Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := extractKey(r)
			if key == "" {
				http.Error(w, `{"error":"missing api key"}`, http.StatusUnauthorized)
				return
			}
			app, err := store.AppByKey(r.Context(), key)
			if err != nil {
				http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), appContextKey, app)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
