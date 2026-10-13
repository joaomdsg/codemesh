package analysis

// pool bounds how many loads run at once. A load holds a go/packages
// process tree, so it is heavy on CPU and memory.
type pool chan struct{}

func (p pool) run(f func()) {
	p <- struct{}{}
	defer func() { <-p }()
	f()
}
