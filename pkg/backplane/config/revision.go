package config

import (
	"strconv"
	"strings"
)

func parseRevision(s string) uint64 {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}

	return n
}
