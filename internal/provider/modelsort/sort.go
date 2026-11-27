// Package modelsort implements version-aware model picker sorting (newest first per family).
package modelsort

import (
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/luank3/gax/internal/provider"
)

// Sort orders entries by family and descending numeric version tokens.
func Sort(entries []provider.ModelEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := parseKey(entries[i].ID), parseKey(entries[j].ID)
		if a.family != b.family {
			return a.family < b.family
		}
		for k := 0; k < len(a.nums) && k < len(b.nums); k++ {
			if a.nums[k] != b.nums[k] {
				return a.nums[k] > b.nums[k]
			}
		}
		if len(a.nums) != len(b.nums) {
			return len(a.nums) > len(b.nums)
		}
		return entries[i].ID < entries[j].ID
	})
}

type sortKey struct {
	family string
	nums   []int
}

func parseKey(id string) sortKey {
	low := strings.ToLower(id)
	var fam strings.Builder
	var nums []int
	for i := 0; i < len(low); {
		if unicode.IsDigit(rune(low[i])) {
			j := i
			for j < len(low) && unicode.IsDigit(rune(low[j])) {
				j++
			}
			if n, err := strconv.Atoi(low[i:j]); err == nil {
				nums = append(nums, n)
			}
			i = j
			continue
		}
		fam.WriteByte(low[i])
		i++
	}
	return sortKey{family: fam.String(), nums: nums}
}
