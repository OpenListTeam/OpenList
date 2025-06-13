package conf

import (
	"net/url"
	"regexp"
)

var (
	BuiltAt    string
	GitAuthor  string
	GitCommit  string
	Version    string = "dev"
	WebVersion string
)

var (
	Conf *Config
	URL  *url.URL
)

var (
	SlicesMap       = make(map[string][]string)
	FilenameCharMap = make(map[string]string)
	PrivacyReg      []*regexp.Regexp
)

// StoragesLoaded loaded success if empty
var StoragesLoaded = false

var (
	RawIndexHtml string
	ManageHtml   string
	IndexHtml    string
)
