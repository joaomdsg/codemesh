// Package live watches the working trees of local repos and keeps the
// engine's snapshots of them current, announcing each change on a topic.
package live

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/lang"
)

// fingerprint is lang's fingerprint of dir plus HEAD, so commits and
// checkouts count as changes.
func fingerprint(dir string, repo *gitx.Repo) string {
	h := sha256.New()
	fmt.Fprintln(h, lang.Fingerprint(dir))
	if repo != nil {
		head, _ := repo.Head()
		fmt.Fprintln(h, head)
	}
	return hex.EncodeToString(h.Sum(nil))
}
