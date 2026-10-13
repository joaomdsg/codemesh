package app

import "time"

// AgePRs makes the repo's cached pull request list d older.
func (a *App) AgePRs(id string, d time.Duration) {
	rs, err := a.repo(id)
	if err != nil {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.prs.at = rs.prs.at.Add(-d)
}
