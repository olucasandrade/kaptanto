package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewRecentIndex_ClampsCapacity(t *testing.T) {
	idx := newRecentIndex(0)
	assert.Equal(t, DefaultRecentIndexSize, idx.cap)
	idx2 := newRecentIndex(-1)
	assert.Equal(t, DefaultRecentIndexSize, idx2.cap)
}

func TestRecentConsumer_CloseNilSafe(t *testing.T) {
	var c *recentConsumer
	c.close()
	c = &recentConsumer{}
	c.close()
}
