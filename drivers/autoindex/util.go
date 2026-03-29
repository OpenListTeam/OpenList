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
			return strings.TrimSpace(s[:i+1]), strings.TrimSpace(s[i+1:])
		}
	}
	return "", s
}

func parseSize(a any) (int64, bool, error) {
	// 第二个返回值exact表示大小是否精确
	if f, ok := a.(float64); ok {
		return int64(f), false, nil
	}
	s, err := parseString(a)
	if errors.Is(err, errEmptyEvaluateResult) {
		// 可能是错误，也可能确实大小为0
		// 如果确实大小为0，大概率不会下载，exact返回false也不会有什么性能损失
		// 如果是错误，exact返回true会导致本地代理出错，综合来看返回false更好
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	s = strings.TrimSpace(s)
	if s == "-" {
		return 0, false, nil
	}
	nbs, unit := splitUnit(s)
	mul, ok := units[strings.ToLower(unit)]
	exact := mul == 1
	if !ok {
		mul = 1
		// 推测无单位，exact应为false
	}
	nb, err := strconv.ParseInt(nbs, 10, 64)
	if err != nil {
		fnb, err := strconv.ParseFloat(nbs, 64)
		if err != nil {
			return 0, false, fmt.Errorf("failed to convert %s to number", nbs)
		}
		nb = int64(fnb * float64(mul))
		exact = false
	} else {
		nb = nb * mul
	}
	return nb, exact, nil
}

func parseString(res any) (string, error) {
	if r, ok := res.(string); ok {
		if len(r) == 0 {
			return "", errEmptyEvaluateResult
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
		return "", errEmptyEvaluateResult
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
