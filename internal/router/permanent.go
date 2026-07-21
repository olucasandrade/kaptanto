package router

import "fmt"

// PermanentDeliveryError is implemented by errors that will never succeed on
// retry (poison events). isPermanentError gains an errors.As-style unwrap
// check for this interface, alongside the existing io.ErrClosedPipe /
// os.ErrDeadlineExceeded checks.
type PermanentDeliveryError interface{ PermanentDelivery() }

// PermanentError is a generic non-retryable delivery error. Consumers may
// return it when an event is structurally invalid and every retry would fail
// the same way (e.g. malformed row JSON that cannot be filtered).
type PermanentError struct {
	Cause string
}

// Error implements the error interface.
func (e *PermanentError) Error() string {
	if e == nil {
		return "permanent delivery error"
	}
	return "permanent delivery error: " + e.Cause
}

// PermanentDelivery marks this error as a PermanentDeliveryError.
func (e *PermanentError) PermanentDelivery() {}

// PermanentFlushError reports that FlushBatch failed permanently for exactly
// one buffered event. The router dead-letters that event and re-delivers the
// rest of the window. Sinks return it INSTEAD of a plain error only when
// retrying cannot help.
type PermanentFlushError struct {
	Seq   uint64 // eventlog seq of the poisoned entry
	Cause error
}

// Error implements the error interface.
func (e *PermanentFlushError) Error() string {
	if e == nil {
		return "permanent flush error"
	}
	if e.Cause == nil {
		return fmt.Sprintf("permanent flush error for seq %d", e.Seq)
	}
	return fmt.Sprintf("permanent flush error for seq %d: %v", e.Seq, e.Cause)
}

// Unwrap returns the underlying cause.
func (e *PermanentFlushError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// PermanentDelivery marks this error as a PermanentDeliveryError.
func (e *PermanentFlushError) PermanentDelivery() {}
