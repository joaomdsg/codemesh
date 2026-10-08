package size

func Nest4(x int) int {
	if x > 0 {
		if x > 1 {
			if x > 2 {
				if x > 3 {
					x++
				}
			}
		}
	}
	return x
}

func Nest5(x int) int {
	if x > 0 {
		if x > 1 {
			if x > 2 {
				if x > 3 {
					if x > 4 {
						x++
					}
				}
			}
		}
	}
	return x
}

func Params5(p0, p1, p2, p3, p4 int) int { return p0 }

func Params6(p0, p1, p2, p3, p4, p5 int) int { return p0 }
