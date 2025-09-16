package main

import "net/http"

func cacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Disable caching for responses handled by this middleware
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
