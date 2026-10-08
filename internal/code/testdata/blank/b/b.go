package b

type T struct{ n int }

func F(t T) int {
	x := t.n
	return x + x
}
