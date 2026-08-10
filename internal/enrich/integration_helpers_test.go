package enrich_test

import (
	"testing"

	"github.com/olucasandrade/kaptanto/internal/event"
	"github.com/olucasandrade/kaptanto/internal/eventlog"
	"github.com/stretchr/testify/require"
)

func materializeEntry(t *testing.T, ent eventlog.LogEntry) *event.ChangeEvent {
	require.NoError(t, ent.MaterializeEvent())
	return ent.Event
}
