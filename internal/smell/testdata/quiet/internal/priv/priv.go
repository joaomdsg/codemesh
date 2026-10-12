package priv

func Used() int { return 1 }

func Orphan() {}

// Mode, Item and Tag are used outside only through a constant, a field
// and a field's value, never by name. Slow and Hot are used nowhere, but
// belong to Mode and Tag.
type Mode int

const (
	Fast Mode = 1
	Slow Mode = 2
)

type Item struct {
	N int
	T Tag
}

type Tag string

const Hot Tag = "hot"

func Get() Item { return Item{} }
