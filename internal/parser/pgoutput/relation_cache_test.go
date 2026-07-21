package pgoutput

import (
	"testing"

	"github.com/jackc/pglogrepl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelationCache_AllAndLookupByName(t *testing.T) {
	c := NewRelationCache()
	c.Set(&pglogrepl.RelationMessageV2{
		RelationMessage: pglogrepl.RelationMessage{
			RelationID:   1,
			Namespace:    "public",
			RelationName: "orders",
			Columns: []*pglogrepl.RelationMessageColumn{
				{Flags: 1, Name: "id", DataType: 23},
			},
		},
	})
	c.Set(&pglogrepl.RelationMessageV2{
		RelationMessage: pglogrepl.RelationMessage{
			RelationID:   2,
			Namespace:    "public",
			RelationName: "users",
			Columns:      []*pglogrepl.RelationMessageColumn{{Name: "id", DataType: 23}},
		},
	})

	all := c.All()
	require.Len(t, all, 2)

	got, ok := c.LookupByName("public", "orders")
	require.True(t, ok)
	assert.Equal(t, uint32(1), got.RelationID)
	assert.Equal(t, "orders", got.RelationName)

	_, ok = c.LookupByName("public", "missing")
	assert.False(t, ok)
}
