package net

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	stderrors "errors"
	"fmt"
	"io"
	"mime/multipart"
	gonet "net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

//this file is inspired by GO_SDK net.http.ServeContent

// ServeHTTP replies to the request using the content in the
// provided range reader. The main benefit of ServeHTTP over io.Copy
// is that it handles Range requests properly, sets the MIME type, and
// handles If-Match, If-Unmodified-Since, If-None-Match, If-Modified-Since,
// and If-Range requests.
//
// If the response's Content-Type header is not set, ServeHTTP
// first tries to deduce the type from name's file extension and,
// if that fails, falls back to reading the first block of the content
// and passing it to DetectContentType.
// The name is otherwise unused; in particular it can be empty and is
// never sent in the response.
//
// If modtime is not the zero time or Unix epoch, ServeHTTP
// includes it in a Last-Modified header in the response. If the
// request includes an If-Modified-Since header, ServeHTTP uses
// modtime to decide whether the content needs to be sent at all.
//
// The content's RangeRead method must return a reader for the requested range.
//
// If the caller has set w's ETag header formatted per RFC 7232, section 2.3,
// ServeHTTP uses it to handle requests using If-Match, If-None-Match, or If-Range.
func ServeHTTP(w http.ResponseWriter, r *http.Request, name string, modTime time.Time, size int64, rangeReader model.RangeReaderIF) (err error) {
	setLastModified(w, modTime)
	done, rangeReq := checkPreconditions(w, r, modTime)
	if done {
		return nil
	}

	if size < 0 {
		// since too many functions need file size to work,
		// will not implement the support of unknown file size here
		http.Error(w, "negative content size not supported", http.StatusInternalServerError)
		return nil
	}

	code := http.StatusOK

	// If Content-Type isn't set, use the file's extension to find it, but
	// if the Content-Type is unset explicitly, do not sniff the type.
	contentTypes, haveType := w.Header()["Content-Type"]
	var contentType string
	if !haveType {
		contentType = utils.GetMimeType(name)
		w.Header().Set("Content-Type", contentType)
	} else if len(contentTypes) > 0 {
		contentType = contentTypes[0]
	}

	// handle Content-Range header.
	sendSize := size
	var sendContent io.ReadCloser
	ranges, err := http_range.ParseRange(rangeReq, size)
	switch {
	case err == nil:
	case errors.Is(err, http_range.ErrNoOverlap):
		if size == 0 {
			// Some clients add a Range header to all requests to
			// limit the size of the response. If the file is empty,
			// ignore the range header and respond with a 200 rather
			// than a 416.
			ranges = nil
			break
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		fallthrough
	default:
		http.Error(w, err.Error(), http.StatusRequestedRangeNotSatisfiable)
		return nil
	}

	if sumRangesSize(ranges) > size {
		// The total number of bytes in all the ranges is larger than the size of the file
		// or unknown file size, ignore the range request.
		ranges = nil
	}

	// 使用请求的Context
	// 不然从sendContent读不到数据，即使请求断开CopyBuffer也会一直堵塞
	ctx := r.Context()
	switch {
	case len(ranges) == 0:
		reader, err := openRange(ctx, rangeReader, http_range.Range{Length: -1})
		if err != nil {
			code = http.StatusRequestedRangeNotSatisfiable
			var statusCode HttpStatusCodeError
			if errors.As(err, &statusCode) {
				code = int(statusCode)
			}
			http.Error(w, err.Error(), code)
			return nil
		}
		sendContent = reader
	case len(ranges) == 1:
		// RFC 7233, Section 4.1:
		// "If a single part is being transferred, the server
		// generating the 206 response MUST generate a
		// Content-Range header field, describing what range
		// of the selected representation is enclosed, and a
		// payload consisting of the range.
		// ...
		// A server MUST NOT generate a multipart response to
		// a request for a single range, since a client that
		// does not request multiple parts might not support
		// multipart responses."
		ra := ranges[0]
		sendContent, err = openRange(ctx, rangeReader, ra)
		if err != nil {
			code = http.StatusRequestedRangeNotSatisfiable
			var statusCode HttpStatusCodeError
			if errors.As(err, &statusCode) {
				code = int(statusCode)
			}
			http.Error(w, err.Error(), code)
			return nil
		}
		sendSize = ra.Length
		code = http.StatusPartialContent
		w.Header().Set("Content-Range", ra.ContentRange(size))
	case len(ranges) > 1:
		sendSize, err = rangesMIMESize(ranges, contentType, size)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestedRangeNotSatisfiable)
		}
		code = http.StatusPartialContent

		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		w.Header().Set("Content-Type", "multipart/byteranges; boundary="+mw.Boundary())
		sendContent = pr
		go func() {
			for _, ra := range ranges {
				part, err := mw.CreatePart(ra.MimeHeader(contentType, size))
				if err != nil {
					pw.CloseWithError(err)
					return
				}
				if err := copyRange(ctx, part, rangeReader, ra); err != nil {
					pw.CloseWithError(err)
					return
				}
			}

			_ = pw.CloseWithError(mw.Close())
		}()
	}
	defer func() {
		err = closeWithError(err, sendContent)
	}()

	w.Header().Set("Accept-Ranges", "bytes")
	if w.Header().Get("Content-Encoding") == "" {
		w.Header().Set("Content-Length", strconv.FormatInt(sendSize, 10))
	}

	w.WriteHeader(code)

	if r.Method != "HEAD" {
		written, err := utils.CopyWithBufferN(w, sendContent, sendSize)
		if err != nil {
			if errors.Is(context.Cause(ctx), context.Canceled) {
				return nil
			}
			log.Warnf("ServeHttp error. err: %s ", err)
			if written != sendSize {
				log.Warnf("Maybe size incorrect or reader not giving correct/full data, or connection closed before finish. written bytes: %d ,sendSize:%d, ", written, sendSize)
			}
			code = http.StatusInternalServerError
			var statusCode HttpStatusCodeError
			if errors.As(err, &statusCode) {
				code = int(statusCode)
			}
			w.WriteHeader(code)
			return err
		}
	}
	return nil
}

func copyRange(ctx context.Context, dst io.Writer, rangeReader model.RangeReaderIF, requested http_range.Range) (err error) {
	reader, err := openRange(ctx, rangeReader, requested)
	if err != nil {
		return err
	}
	defer func() {
		err = closeWithError(err, reader)
	}()
	_, err = utils.CopyWithBufferN(dst, reader, requested.Length)
	return err
}

func openRange(ctx context.Context, rangeReader model.RangeReaderIF, requested http_range.Range) (io.ReadCloser, error) {
	reader, err := rangeReader.RangeRead(ctx, requested)
	if err != nil {
		if reader != nil {
			err = closeWithError(err, reader)
		}
		return nil, err
	}
	if reader == nil {
		return nil, errors.New("range reader returned a nil body")
	}
	return reader, nil
}

func closeWithError(err error, closer io.Closer) error {
	closeErr := closer.Close()
	if err == nil {
		return closeErr
	}
	if closeErr == nil {
		return err
	}
	return stderrors.Join(err, closeErr)
}
func ProcessHeader(origin, override http.Header) http.Header {
	result := http.Header{}
	// client header
	for h, val := range origin {
		if utils.SliceContains(conf.SlicesMap[conf.ProxyIgnoreHeaders], strings.ToLower(h)) {
			continue
		}
		result[h] = val
	}
	// needed header
	for h, val := range override {
		result[h] = val
	}
	return result
}

// RequestHttp deal with Header properly then send the request
func RequestHttp(ctx context.Context, httpMethod string, headerOverride http.Header, URL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, httpMethod, URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header = headerOverride
	res, err := HttpClient().Do(req)
	if err != nil {
		return nil, err
	}
	// TODO clean header with blocklist or passlist
	res.Header.Del("set-cookie")
	var reader io.Reader
	if res.StatusCode >= 400 {
		// 根据 Content-Encoding 判断 Body 是否压缩
		switch res.Header.Get("Content-Encoding") {
		case "gzip":
			// 使用gzip.NewReader解压缩
			reader, _ = gzip.NewReader(res.Body)
			defer reader.(*gzip.Reader).Close()
		default:
			// 没有Content-Encoding，直接读取
			reader = res.Body
		}
		all, _ := io.ReadAll(reader)
		_ = res.Body.Close()
		msg := string(all)
		log.Debugln(msg)
		return nil, fmt.Errorf("http request [%s] failure,status: %w response:%s", URL, HttpStatusCodeError(res.StatusCode), msg)
	}
	return res, nil
}

type HttpStatusCodeError int

func (e HttpStatusCodeError) Error() string {
	return fmt.Sprintf("%d|%s", e, http.StatusText(int(e)))
}

var once sync.Once
var httpClient *http.Client

func HttpClient() *http.Client {
	once.Do(func() {
		httpClient = NewHttpClient()
		httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			req.Header.Del("Referer")
			return nil
		}
	})
	return httpClient
}

func NewHttpClient() *http.Client {
	transport := &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: conf.Conf.TlsInsecureSkipVerify},
	}

	SetProxyIfConfigured(transport)

	return &http.Client{
		Timeout:   time.Hour * 48,
		Transport: &safeTransport{base: transport},
	}
}

type safeTransport struct {
	base http.RoundTripper
}

func (t *safeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	addrs, err := gonet.DefaultResolver.LookupIPAddr(req.Context(), host)
	if err != nil || len(addrs) == 0 {
		return nil, errors.Wrapf(err, "failed to resolve host: %s", host)
	}
	for _, addr := range addrs {
		if isCloudMetadataIP(addr.IP) {
			return nil, ErrCloudMetadataEndpoint
		}
	}
	return t.base.RoundTrip(req)
}

var ErrCloudMetadataEndpoint = errors.New("access to cloud metadata endpoint is not allowed")

func isCloudMetadataIP(ip gonet.IP) bool {
	ip = ip.To4()
	return ip != nil && ip[0] == 169 && ip[1] == 254 && ip[2] == 169 && ip[3] == 254
}
