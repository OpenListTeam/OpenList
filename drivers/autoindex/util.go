package autoindex

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/antchfx/xpath"
	"github.com/pkg/errors"
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

func parseSize(a any) (int64, error) {
	if f, ok := a.(float64); ok {
		return int64(f), nil
	}
	s, err := parseString(a)
	if err != nil {
		return 0, err
	}
	s = strings.TrimSpace(s)
	if s == "-" {
		return 0, nil
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
			return 0, fmt.Errorf("failed to convert %s to number", nbs)
		}
		nb = int64(fnb * float64(mul))
	} else {
		nb = nb * mul
	}
	return nb, nil
}

func parseString(res any) (string, error) {
	if r, ok := res.(string); ok {
		if len(r) == 0 {
			return "", fmt.Errorf("empty result")
		}
		return r, nil
	}
	n, ok := res.(*xpath.NodeIterator)
	if !ok {
		return "", fmt.Errorf("unsupported evaluating result")
	}
	if !n.MoveNext() {
		return "", fmt.Errorf("no matched nodes")
	}
	ns := n.Current().Value()
	if len(ns) == 0 {
		return "", fmt.Errorf("empty result")
	}
	return ns, nil
}

func parseTime(res any, format string) (time.Time, error) {
	s, err := parseString(res)
	if err != nil {
		return time.Now(), err
	}
	s = strings.TrimSpace(s)
	t, err := time.Parse(format, s)
	if err != nil {
		return time.Now(), errors.WithMessagef(err, "failed to convert %s to time", s)
	}
	return t, nil
}
