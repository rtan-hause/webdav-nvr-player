// Package httpx holds the JSON response helpers shared by the HTTP handlers.
package httpx

import (
	"encoding/json"
	"net/http"
)

// JSON writes v as a JSON response with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes {"error": msg}, the shape the web UI reads error messages from.
func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}
