package vector

import (
	"errors"
	"fmt"
	"net/http"
)

// StatusError wraps a non-2xx HTTP response from an embedder or store.
// Consumers classify 408/429/5xx as transient and other 4xx as poison.
type StatusError struct {
	Status int
	Msg    string
}

func (e *StatusError) Error() string {
	if e == nil {
		return "vector: status error: <nil>"
	}
	if e.Msg == "" {
		return fmt.Sprintf("vector: HTTP %d", e.Status)
	}
	return fmt.Sprintf("vector: HTTP %d: %s", e.Status, e.Msg)
}

// IsTransient reports whether err should block+backoff (never DLQ).
// 408/429/5xx StatusErrors and all non-StatusError failures (network,
// count-mismatch, etc.) are transient. Other 4xx are not.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var se *StatusError
	if errors.As(err, &se) {
		return se.Status == http.StatusRequestTimeout ||
			se.Status == http.StatusTooManyRequests ||
			se.Status >= 500
	}
	return true
}

// IsPoison reports whether err is a non-retryable 4xx (other than 408/429).
func IsPoison(err error) bool {
	if err == nil {
		return false
	}
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	return se.Status >= 400 && se.Status < 500 &&
		se.Status != http.StatusRequestTimeout &&
		se.Status != http.StatusTooManyRequests
}
