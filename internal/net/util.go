package net

import (
	"io"
	"mime/multipart"
	"net/http"
	"net/url"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/rclone/rclone/lib/readers"

	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

func sumRangesSize(ranges []http_range.Range) (size int64) {
	for _, ra := range ranges {
		size += ra.Length
	}
	return
}

// countingWriter counts how many bytes have been written to it.
type countingWriter int64

func (w *countingWriter) Write(p []byte) (n int, err error) {
	*w += countingWriter(len(p))
	return len(p), nil
}

// rangesMIMESize returns the number of bytes it takes to encode the
// provided ranges as a multipart response.
func rangesMIMESize(ranges []http_range.Range, contentType string, contentSize int64) (encSize int64, err error) {
	var w countingWriter
	mw := multipart.NewWriter(&w)
	for _, ra := range ranges {
		_, err := mw.CreatePart(ra.MimeHeader(contentType, contentSize))
		if err != nil {
			return 0, err
		}
		encSize += ra.Length
	}
	err = mw.Close()
	if err != nil {
		return 0, err
	}
	encSize += int64(w)
	return encSize, nil
}

// GetRangedHttpReader some http server doesn't support "Range" header,
// so this function read readCloser with whole data, skip offset, then return ReaderCloser.
func GetRangedHttpReader(readCloser io.ReadCloser, offset, length int64) (io.ReadCloser, error) {

	if offset > 100*1024*1024 {
		log.Warnf("offset is more than 100MB, if loading data from internet, high-latency and wasting of bandwidth is expected")
	}

	if _, err := utils.CopyWithBuffer(io.Discard, io.LimitReader(readCloser, offset)); err != nil {
		return nil, err
	}

	// return an io.ReadCloser that is limited to `length` bytes.
	return readers.NewLimitedReadCloser(readCloser, length), nil
}

// SetProxyIfConfigured sets proxy for HTTP Transport if configured
func SetProxyIfConfigured(transport *http.Transport) {
	// If proxy address is configured, override environment variable settings
	if conf.Conf.ProxyAddress != "" {
		if proxyURL, err := url.Parse(conf.Conf.ProxyAddress); err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}
}

// SetRestyProxyIfConfigured sets proxy for Resty client if configured
func SetRestyProxyIfConfigured(client *resty.Client) {
	// If proxy address is configured, override environment variable settings
	if conf.Conf.ProxyAddress != "" {
		if proxyURL, err := url.Parse(conf.Conf.ProxyAddress); err == nil {
			client.SetProxy(proxyURL.String())
		}
	}
}
