package router

import (
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
)

func TestIsPermanentErrorRecognizesPermanentDeliveryError(t *testing.T) {
	t.Parallel()

	plain := errors.New("transient")
	if isPermanentError(plain) {
		t.Fatal("plain error must not be permanent")
	}
	if isPermanentError(nil) {
		t.Fatal("nil must not be permanent")
	}

	perm := &PermanentFlushError{Seq: 42, Cause: errors.New("poison")}
	if !isPermanentError(perm) {
		t.Fatal("PermanentFlushError must be permanent")
	}

	wrapped := fmt.Errorf("flush failed: %w", perm)
	if !isPermanentError(wrapped) {
		t.Fatal("wrapped PermanentFlushError must be permanent via unwrap chain")
	}

	if !isPermanentError(io.ErrClosedPipe) {
		t.Fatal("io.ErrClosedPipe must remain permanent")
	}
	if !isPermanentError(os.ErrDeadlineExceeded) {
		t.Fatal("os.ErrDeadlineExceeded must remain permanent")
	}
	if !isPermanentError(fmt.Errorf("wrap: %w", io.ErrClosedPipe)) {
		t.Fatal("wrapped io.ErrClosedPipe must remain permanent")
	}
}

func TestPermanentFlushErrorMethods(t *testing.T) {
	t.Parallel()
	err := &PermanentFlushError{Seq: 7, Cause: errors.New("bad")}
	if got := err.Error(); got == "" {
		t.Fatal("Error() empty")
	}
	if !errors.Is(err, err.Cause) {
		t.Fatal("Unwrap/Is must reach Cause")
	}
	var _ PermanentDeliveryError = err
	err.PermanentDelivery()
}
