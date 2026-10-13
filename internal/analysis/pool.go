package analysis

import "context"

// pool bounds how many loads run at once. A load holds a go/packages
// process tree, so it is heavy on CPU and memory.
type pool chan struct{}

// run calls f once a slot is free, or returns ctx's error if it ends first.
func (p pool) run(ctx context.Context, f func()) error {
	select {
	case p <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-p }()
	f()
	return nil
}
