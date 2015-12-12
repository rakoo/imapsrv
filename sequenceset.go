package unpeu

import (
	"sort"
	"strconv"
	"strings"
)

type sequenceset struct {
}

func toList(sequenceSet string, max int) ([]int, error) {
	parts := strings.Split(sequenceSet, ",")
	all := make(map[int]struct{})

	for _, part := range parts {
		if colon := strings.Index(part, ":"); colon > 0 {
			leftStr := part[:colon]
			rightStr := part[colon+1:]

			// Convert to max
			if leftStr == "*" {
				leftStr = strconv.Itoa(max)
			} else if rightStr == "*" {
				rightStr = strconv.Itoa(max)
			}

			left, err := strconv.Atoi(leftStr)
			if err != nil {
				return nil, err
			}
			right, err := strconv.Atoi(rightStr)
			if err != nil {
				return nil, err
			}

			// If the non-converted is over max, cap it
			if left > max && right == max {
				all[max] = struct{}{}
				continue
			} else if right > max && left == max {
				all[max] = struct{}{}
				continue
			}

			// If the part is impossible, discard it
			if left > max && right > max {
				continue
			}

			from := left
			to := right
			if from > right {
				from = right
				to = left
			}

			for i := from; i <= to; i++ {
				all[i] = struct{}{}
			}
		} else if part == "*" {
			all[max] = struct{}{}
		} else {
			i, err := strconv.Atoi(part)
			if err != nil {
				return nil, err
			}
			all[i] = struct{}{}
		}
	}

	out := make([]int, 0, len(all))
	for k := range all {
		out = append(out, k)
	}
	sort.Ints(out)
	return out, nil
}
