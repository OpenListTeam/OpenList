package common

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"maps"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func Proxy(w http.ResponseWriter, r *http.Request, link *model.Link, file model.Obj) error {
	if link.MFile != nil {
		attachHeader(w, file, link.Header)
		http.ServeContent(w, r, file.GetName(), file.ModTime(), link.MFile)
		return nil
	}

	if link.Concurrency > 0 || link.PartSize > 0 {
		attachHeader(w, file, link.Header)
		rrf, _ := stream.GetRangeReaderFromLink(file.GetSize(), link)
		if link.RangeReader == nil {
			r = r.WithContext(context.WithValue(r.Context(), conf.RequestHeaderKey, r.Header))
		}
		return net.ServeHTTP(w, r, file.GetName(), file.ModTime(), file.GetSize(), &model.RangeReadCloser{
			RangeReader: rrf,
		})
	}

	if link.RangeReader != nil {
		attachHeader(w, file, link.Header)
		return net.ServeHTTP(w, r, file.GetName(), file.ModTime(), file.GetSize(), &model.RangeReadCloser{
			RangeReader: link.RangeReader,
		})
	}

	//transparent proxy
	header := net.ProcessHeader(r.Header, link.Header)
	res, err := net.RequestHttp(r.Context(), r.Method, header, link.URL)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	maps.Copy(w.Header(), res.Header)
	w.WriteHeader(res.StatusCode)
	if r.Method == http.MethodHead {
		return nil
	}
	_, err = utils.CopyWithBuffer(w, &stream.RateLimitReader{
		Reader:  res.Body,
		Limiter: stream.ServerDownloadLimit,
		Ctx:     r.Context(),
	})
	return err
}
func attachHeader(w http.ResponseWriter, file model.Obj, header http.Header) {
	fileName := file.GetName()
	w.Header().Set("Content-Disposition", utils.GenerateContentDisposition(fileName))
	w.Header().Set("Content-Type", utils.GetMimeType(fileName))
	w.Header().Set("Etag", GetEtag(file))
	contentType := header.Get("Content-Type")
	if len(contentType) > 0 {
		w.Header().Set("Content-Type", contentType)
	}
}
func GetEtag(file model.Obj) string {
	hash := ""
	for _, v := range file.GetHash().Export() {
		if strings.Compare(v, hash) > 0 {
			hash = v
		}
	}
	if len(hash) > 0 {
		return fmt.Sprintf(`"%s"`, hash)
	}
	// 参考nginx
	return fmt.Sprintf(`"%x-%x"`, file.ModTime().Unix(), file.GetSize())
}

func ProxyRange(ctx context.Context, link *model.Link, size int64) {
	if link.MFile != nil {
		return
	}
	if link.RangeReader == nil && !strings.HasPrefix(link.URL, GetApiUrl(ctx)+"/") {
		rrf, err := stream.GetRangeReaderFromLink(size, link)
		if err != nil {
			return
		}
		link.RangeReader = rrf
	}
}

type InterceptResponseWriter struct {
	http.ResponseWriter
	io.Writer
}

func (iw *InterceptResponseWriter) Write(p []byte) (int, error) {
	return iw.Writer.Write(p)
}

type WrittenResponseWriter struct {
	http.ResponseWriter
	written bool
}

func (ww *WrittenResponseWriter) Write(p []byte) (int, error) {
	n, err := ww.ResponseWriter.Write(p)
	if !ww.written && n > 0 {
		ww.written = true
	}
	return n, err
}

func (ww *WrittenResponseWriter) IsWritten() bool {
	return ww.written
}
