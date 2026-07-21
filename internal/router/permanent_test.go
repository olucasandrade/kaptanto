package router

import (
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
)

func TestIsPermanentError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "plain error is not permanent",
			err:  errors.New("transient"),
			want: false,
		},
		{
			name: "nil is not permanent",
			err:  nil,
			want: false,
		},
		{
			name: "PermanentFlushError is permanent",
			err:  &PermanentFlushError{Seq: 42, Cause: errors.New("poison")},
			want: true,
		},
		{
			name: "wrapped PermanentFlushError is permanent",
			err:  fmt.Errorf("flush failed: %w", &PermanentFlushError{Seq: 42, Cause: errors.New("poison")}),
			want: true,
		},
		{
			name: "io.ErrClosedPipe is permanent",
			err:  io.ErrClosedPipe,
			want: true,
		},
		{
			name: "os.ErrDeadlineExceeded is permanent",
			err:  os.ErrDeadlineExceeded,
			want: true,
		},
		{
			name: "wrapped io.ErrClosedPipe is permanent",
			err:  fmt.Errorf("wrap: %w", io.ErrClosedPipe),
			want: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isPermanentError(tt.err); got != tt.want {
				t.Fatalf("isPermanentError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
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
