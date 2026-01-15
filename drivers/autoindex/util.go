package autoindex

import (
	"strconv"
	"strings"
)

var units = map[string]int64{
	"":      1,
	"b":     1,
	"byte":  1,
	"bytes": 1,
	"k":     1 << 10,
	"kb":    1 << 10,
	"kib":   1 << 10,
	"m":     1 << 20,
	"mb":    1 << 20,
	"mib":   1 << 20,
	"g":     1 << 30,
	"gb":    1 << 30,
	"gib":   1 << 30,
	"t":     1 << 40,
	"tb":    1 << 40,
	"tib":   1 << 40,
	"p":     1 << 50,
	"pb":    1 << 50,
	"pib":   1 << 50,
}

func splitUnit(s string) (string, string) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] >= '0' && s[i] <= '9' {
			return s[:i+1], s[i+1:]
		}
	}
	return "", s
}

func ParseSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "-" {
		return 0
	}
	nbs, unit := splitUnit(s)
	mul, ok := units[strings.ToLower(unit)]
	if !ok {
		mul = 1
	}
	nb, err := strconv.ParseInt(nbs, 10, 64)
	if err != nil {
		fnb, err := strconv.ParseFloat(nbs, 64)
		if err != nil {
			return 0
		}
		nb = int64(fnb * float64(mul))
	} else {
		nb = nb * mul
	}
	return nb
}
