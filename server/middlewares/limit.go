package middlewares

import (
	"io"
	"net/http"
	"sync/atomic"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/gin-gonic/gin"
)

func MaxAllowed(n int) gin.HandlerFunc {
	sem := make(chan struct{}, n)
	acquire := func() { sem <- struct{}{} }
	release := func() { <-sem }
	return func(c *gin.Context) {
		acquire()
		defer release()
		c.Next()
	}
}

var maxConnCounter atomic.Int32

func MaxAllowedLive() gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := conf.Conf.MaxConnections
		if limit <= 0 {
			c.Next()
			return
		}
		current := maxConnCounter.Add(1)
		if current > int32(limit) {
			maxConnCounter.Add(-1)
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		defer maxConnCounter.Add(-1)
		c.Next()
	}
}

func UploadRateLimiter(limiter stream.Limiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = &stream.RateLimitReader{
			Reader:  c.Request.Body,
			Limiter: limiter,
			Ctx:     c,
		}
		c.Next()
	}
}

type ResponseWriterWrapper struct {
	gin.ResponseWriter
	WrapWriter io.Writer
}

func (w *ResponseWriterWrapper) Write(p []byte) (n int, err error) {
	return w.WrapWriter.Write(p)
}

func DownloadRateLimiter(limiter stream.Limiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer = &ResponseWriterWrapper{
			ResponseWriter: c.Writer,
			WrapWriter: &stream.RateLimitWriter{
				Writer:  c.Writer,
				Limiter: limiter,
				Ctx:     c,
			},
		}
		c.Next()
	}
}
