package handles

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/thumb"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

var heifThumbExts = map[string]bool{
	"heic": true,
	"heif": true,
	"avif": true,
	"vvc":  true,
	"avc":  true,
}

func isHeifThumb(name string) bool {
	return heifThumbExts[utils.Ext(name)]
}

func Thumb(c *gin.Context) {
	rawPath := c.Request.Context().Value(conf.PathKey).(string)

	size, _ := strconv.Atoi(c.Query("size"))
	if size <= 0 {
		size = 256
	}
	if size > 2560 {
		size = 2560
	}

	link, _, err := fs.Link(c.Request.Context(), rawPath, model.LinkArgs{
		Header: c.Request.Header,
	})
	if err != nil {
		common.ErrorPage(c, err, http.StatusInternalServerError)
		return
	}
	defer link.Close()

	if !isHeifThumb(rawPath) {
		common.ErrorPage(c, errors.New("only heif/heic/avif thumbnail supported"), http.StatusBadRequest)
		return
	}

	cachePath, err := thumb.GenerateHEIFThumb(c.Request.Context(), link, rawPath, size)
	if err != nil {
		common.ErrorPage(c, err, http.StatusInternalServerError)
		return
	}

	c.Header("Cache-Control", "public, max-age=86400")
	c.Header("Content-Type", "image/jpeg")
	c.File(cachePath)
}
