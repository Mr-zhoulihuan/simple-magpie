package prompt

import "sort"

// Headroom estimates how many more requests fit in a window, given the
// session's per-turn token history and the free tokens left. It uses the
// median positive growth between consecutive turns so one huge tool output
// does not throw the estimate off. n is 0 when there is no growth yet.
func Headroom(series []int, free int) int {
	if len(series) < 2 || free <= 0 {
		return 0
	}
	var steps []int
	for i := 1; i < len(series); i++ {
		d := series[i] - series[i-1]
		// ignore a shrunk turn (a compaction) and a jump from a tiny base
		if d > 0 && series[i-1] > 0 && float64(series[i]) >= float64(series[i-1])*0.6 {
			steps = append(steps, d)
		}
	}
	if len(steps) == 0 {
		return 0
	}
	g := median(steps)
	if g <= 0 {
		return 0
	}
	return free / g
}

func median(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int(nil), xs...)
	sort.Ints(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}
