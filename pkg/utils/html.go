package utils

import "github.com/microcosm-cc/bluemonday"

var htmlSanitizePolicy = bluemonday.StrictPolicy()

func SanitizeHTML(s string) string {
	return htmlSanitizePolicy.Sanitize(s)
}
