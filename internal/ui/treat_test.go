package ui

import (
	"errors"
	"testing"

	"github.com/joaomdsg/codemesh/internal/fix"
	"github.com/stretchr/testify/assert"
)

// done is a finished run that may become a pull request.
func done() fix.Snapshot {
	return fix.Snapshot{State: fix.Done, Before: &fix.Side{}, After: &fix.Side{CheckOK: true}, Base: "main", Origin: true}
}

func TestNoPR_saysWhyARunOffersNoPullRequest(t *testing.T) {
	with := func(f func(*fix.Snapshot)) fix.Snapshot { s := done(); f(&s); return s }
	for want, s := range map[string]fix.Snapshot{
		"":                    done(),
		"Opening a draft PR…": with(func(s *fix.Snapshot) { s.PR.Opening = true }),
		"No PR: the repository was on no branch when the run started.": with(func(s *fix.Snapshot) { s.Base = "" }),
		"No PR: the repository has no origin remote.":                  with(func(s *fix.Snapshot) { s.Origin = false }),
		"A PR needs the checks to pass.":                               with(func(s *fix.Snapshot) { s.After.CheckOK = false }),
	} {
		assert.Equal(t, want, noPR(s))
	}
	assert.Empty(t, noPR(fix.Snapshot{State: fix.Working}), "nothing while the run goes")
	assert.Empty(t, noPR(fix.Snapshot{State: fix.Stopped, After: &fix.Side{}}), "nor for a run stopped while Claude worked")
	assert.Empty(t, noPR(fix.Snapshot{State: fix.Failed}))
}

func TestStoppable_showsStopUntilItIsPressed(t *testing.T) {
	for _, c := range []struct {
		s    fix.Snapshot
		stop bool
	}{
		{fix.Snapshot{State: fix.Preparing}, true},
		{fix.Snapshot{State: fix.Working}, true},
		{fix.Snapshot{State: fix.Checking}, true},
		{fix.Snapshot{State: fix.Checking, Stopping: true}, false},
		{fix.Snapshot{State: fix.Preparing, Stopping: true}, false},
		{done(), false},
	} {
		assert.Equal(t, c.stop, stoppable(c.s), "%s stopping=%v", c.s.State, c.s.Stopping)
	}
}

func TestRunNote_saysAStopIsComing(t *testing.T) {
	events := []fix.Event{{Text: "Analysing the result and running the checks again."}}
	assert.Equal(t, "Stopping once the checks finish.", runNote(fix.Snapshot{State: fix.Checking, Stopping: true, Events: events}))
	assert.Equal(t, events[0].Text, runNote(fix.Snapshot{State: fix.Checking, Events: events}))
	assert.Equal(t, events[0].Text, runNote(fix.Snapshot{State: fix.Stopped, Events: events}), "a stopped run still checks what it has")
	assert.Empty(t, runNote(fix.Snapshot{State: fix.Done, Events: events}))
}

func TestBanners_sayWhatWentWrong(t *testing.T) {
	assert.Empty(t, banners(done()))
	assert.Equal(t, []banner{{"hint", "Stopped early. Below is what Claude had changed by then, checked the same way."}}, banners(fix.Snapshot{State: fix.Stopped, Round: 1}))
	assert.Equal(t, []banner{{"hint", "Stopped early. The round you stopped left more than the one before, so it was undone; below is the round before it."}},
		banners(fix.Snapshot{State: fix.Stopped, Round: 2, Undone: 2}))
	assert.Equal(t, []banner{{"hint", "Stopped before Claude started; nothing changed."}}, banners(fix.Snapshot{State: fix.Stopped}))
	assert.Equal(t, []banner{{"banner err", "It did not finish: boom. Nothing in your files changed. Discard and try again."}},
		banners(fix.Snapshot{State: fix.Failed, Err: errors.New("boom")}))
	s := done()
	s.PR.Err = errors.New("push refused.")
	assert.Equal(t, []banner{{"banner err", "The pull request did not open: push refused. The change is still here; you can try again."}}, banners(s))
}
