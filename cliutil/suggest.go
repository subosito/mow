package cliutil

import "strings"

// SuggestCommand returns the closest match to name among cands, or "" when
// nothing is near enough to be a likely typo.
//
// Every mow host needs the same "did you mean" hint for an unknown subcommand.
// Keeping one implementation here means the two binaries cannot drift on how
// forgiving that hint is.
func SuggestCommand(name string, cands []string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	best, bestD := "", 3
	for _, c := range cands {
		d := editDistance(name, c)
		if d > 0 && d < bestD {
			bestD, best = d, c
		}
	}
	if bestD <= 2 {
		return best
	}
	return ""
}

// editDistance is Levenshtein distance, bounded DP sized for short command names.
func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			ins, del, sub := cur[j-1]+1, prev[j]+1, prev[j-1]+cost
			cur[j] = min(ins, min(del, sub))
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
