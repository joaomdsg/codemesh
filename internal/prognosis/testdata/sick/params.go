package sick

func Six(a, b, c, d, e, f int) int         { return a + b + c + d + e + f }
func Seven(a, b, c, d, e, f, g int) int    { return Six(a, b, c, d, e, f) + g }
func Eight(a, b, c, d, e, f, g, h int) int { return Seven(a, b, c, d, e, f, g) + h }
