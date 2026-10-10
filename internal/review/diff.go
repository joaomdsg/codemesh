package review

// maxCells bounds the LCS table; past it a unit shows as removed then added.
const maxCells = 4_000_000

// diffLines is a longest-common-subsequence line diff of a unit's old and new
// text. Each side starts at the given file line. Deletions come before the
// insertions that replace them.
func diffLines(a, b []string, aStart, bStart int) []Line {
	if len(a)*len(b) > maxCells {
		return append(lines(a, '-', aStart), lines(b, '+', bStart)...)
	}
	lcs := lcsTable(a, b)
	var out []Line
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			out = append(out, Line{Op: ' ', Old: aStart + i, New: bStart + j, Text: a[i]})
			i++
			j++
		case i < len(a) && (j == len(b) || lcs[i+1][j] >= lcs[i][j+1]):
			out = append(out, Line{Op: '-', Old: aStart + i, Text: a[i]})
			i++
		default:
			out = append(out, Line{Op: '+', New: bStart + j, Text: b[j]})
			j++
		}
	}
	return out
}

// lcsTable returns t where t[i][j] is the LCS length of a[i:] and b[j:].
func lcsTable(a, b []string) [][]int {
	t := make([][]int, len(a)+1)
	for i := range t {
		t[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				t[i][j] = t[i+1][j+1] + 1
			} else {
				t[i][j] = max(t[i+1][j], t[i][j+1])
			}
		}
	}
	return t
}

func lines(text []string, op byte, start int) []Line {
	out := make([]Line, len(text))
	for i, t := range text {
		l := Line{Op: op, Text: t}
		if op == '-' {
			l.Old = start + i
		} else {
			l.New = start + i
		}
		out[i] = l
	}
	return out
}
