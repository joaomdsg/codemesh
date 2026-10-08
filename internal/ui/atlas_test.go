package ui

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The atlas tile's wire form is an array the island indexes by position;
// atlas.js reads [k, x, y, w, h, name, unit].
func TestAtlasTile_marshalsAsTheArrayTheIslandReads(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(atlasTile{K: "d", X: 1, Y: 2, W: 3, H: 4.5, Name: "Scale", Unit: "u1"})
	require.NoError(t, err)
	assert.JSONEq(t, `["d", 1, 2, 3, 4.5, "Scale", "u1"]`, string(b))
}
