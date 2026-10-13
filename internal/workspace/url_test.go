package workspace

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseURL(t *testing.T) {
	t.Parallel()
	for arg, want := range map[string]remote{
		"https://github.com/acme/widget.git": {"https://github.com/acme/widget.git", "github.com", "acme", "widget"},
		"https://github.com/acme/widget/":    {"https://github.com/acme/widget/", "github.com", "acme", "widget"},
		"ssh://git@host:22/acme/widget":      {"ssh://git@host:22/acme/widget", "host", "acme", "widget"},
		"git@github.com:acme/widget.git":     {"git@github.com:acme/widget.git", "github.com", "acme", "widget"},
		"file:///srv/acme/widget.git":        {"file:///srv/acme/widget.git", "local", "acme", "widget"},
	} {
		got, ok := parseURL(arg)
		assert.True(t, ok, arg)
		assert.Equal(t, want, got, arg)
	}
	for _, arg := range []string{"/home/me/repo", "relative/repo", "../x", "C:/x", "https://github.com/"} {
		_, ok := parseURL(arg)
		assert.False(t, ok, arg)
	}
}
