package s

func Dispatch(name string) int {
	switch name {
	case "c0":
		return 0
	case "c1":
		return 1
	case "c2":
		return 2
	case "c3":
		return 3
	case "c4":
		return 4
	case "c5":
		return 5
	case "c6":
		return 6
	case "c7":
		return 7
	case "c8":
		return 8
	case "c9":
		return 9
	case "c10":
		return 10
	case "c11":
		return 11
	case "c12":
		return 12
	case "c13":
		return 13
	}
	return -1
}

func Kind(x any) int {
	switch x.(type) {
	case int:
		return 1
	case string:
		return 2
	default:
		return 0
	}
}

func Wait(a, b chan int) int {
	select {
	case v := <-a:
		return v
	case v := <-b:
		return v
	}
}
