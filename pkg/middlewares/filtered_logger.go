package middlewares

import (
	"net/netip"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type filter struct {
	CIDR   *netip.Prefix `json:"cidr,omitempty"`
	Path   *string       `json:"path,omitempty"`
	Method *string       `json:"method,omitempty"`
}

var filterList []*filter

func initFilterList() {
	for _, s := range conf.Conf.Log.Filter.Filters {
		f := new(filter)

		if s.CIDR != "" {
			cidr, err := netip.ParsePrefix(s.CIDR)
			if err != nil {
				log.Errorf("failed to parse CIDR %s: %v", s.CIDR, err)
				continue
			}
			f.CIDR = &cidr
		}

		if s.Path != "" {
			f.Path = &s.Path
		}

		if s.Method != "" {
			f.Method = &s.Method
		}

		if f.CIDR == nil && f.Path == nil && f.Method == nil {
			log.Warnf("filter %s is empty, skipping", s)
			continue
		}

		filterList = append(filterList, f)
		log.Debugf("added filter: %+v", f)
	}

	log.Infof("Loaded %d log filters.", len(filterList))
}

func skiperDecider(c *gin.Context) bool {
	// every filter need metch all condithon as filter match
	// so if any condithon not metch, skip this filter
	// all filters misatch, log this request

	for _, f := range filterList {
		if f.CIDR != nil {
			cip := netip.MustParseAddr(c.ClientIP())
			if !f.CIDR.Contains(cip) {
				continue
			}
		}

		if f.Path != nil {
			if (*f.Path)[0] == '/' {
				// match path as prefix/exact path
				if !strings.HasPrefix(c.Request.URL.Path, *f.Path) {
					continue
				}
			} else {
				// match path as relative path
				if !strings.Contains(c.Request.URL.Path, "/"+*f.Path) {
					continue
				}
			}
		}

		if f.Method != nil {
			if *f.Method != c.Request.Method {
				continue
			}
		}

		return true
	}

	return false
}

func FilteredLogger() gin.HandlerFunc {
	initFilterList()

	return gin.LoggerWithConfig(gin.LoggerConfig{
		Output: log.StandardLogger().Out,
		Skip:   skiperDecider,
	})
}
