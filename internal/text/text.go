// Package text holds the small formatting helpers shared by the apply
// summaries and the terminal UI, which cannot import each other.
package text

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// HumanBytes formats a byte count as "512 B", "2.0 KB", "5.0 MB" and so on.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	value := float64(n)
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	i := -1
	for value >= unit && i < len(units)-1 {
		value /= unit
		i++
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + " " + units[i]
}

// JoinCounts renders a count map as "key n, key n", sorted by key.
func JoinCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}
