package feishu

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

type exportEntry struct {
	done    chan struct{}
	token   string
	size    int64
	expires time.Time
	err     error
}

func (s *session) export(ctx context.Context, o *object) (string, int64, error) {
	key := o.Kind + ":" + o.ID + ":" + s.extension(o.Kind) + ":" + strconv.FormatInt(o.Modified.Unix(), 10)
	s.exportMu.Lock()
	if cached := s.exports[key]; cached != nil {
		select {
		case <-cached.done:
			if cached.err == nil && time.Now().Before(cached.expires) {
				s.exportMu.Unlock()
				return cached.token, cached.size, nil
			}
		default:
			s.exportMu.Unlock()
			select {
			case <-ctx.Done():
				return "", 0, ctx.Err()
			case <-cached.done:
				return cached.token, cached.size, cached.err
			}
		}
	}
	// Bound retained metadata; completed entries may be evicted, active jobs may not.
	for k, e := range s.exports {
		select {
		case <-e.done:
			if time.Now().After(e.expires) || len(s.exports) >= 128 {
				delete(s.exports, k)
			}
		default:
		}
	}
	if len(s.exports) >= 128 {
		s.exportMu.Unlock()
		return "", 0, fmt.Errorf("too many concurrent Feishu export tasks; retry later")
	}
	entry := &exportEntry{done: make(chan struct{})}
	s.exports[key] = entry
	s.exportMu.Unlock()
	entry.token, entry.size, entry.err = s.runExport(ctx, o)
	entry.expires = time.Now().Add(time.Minute)
	close(entry.done)
	return entry.token, entry.size, entry.err
}

func (s *session) runExport(ctx context.Context, o *object) (string, int64, error) {
	var created struct {
		Ticket string `json:"ticket"`
	}
	if err := s.json(ctx, http.MethodPost, "/drive/v1/export_tasks", map[string]string{"token": o.ID, "type": o.Kind, "file_extension": s.extension(o.Kind)}, &created); err != nil {
		return "", 0, err
	}
	if !validID(created.Ticket) {
		return "", 0, fmt.Errorf("invalid Feishu export ticket")
	}
	for {
		var response struct {
			Result *struct {
				Status *int   `json:"job_status"`
				Token  string `json:"file_token"`
				Size   *int64 `json:"file_size"`
			} `json:"result"`
		}
		if err := s.json(ctx, http.MethodGet, "/drive/v1/export_tasks/"+created.Ticket+"?token="+o.ID, nil, &response); err != nil {
			return "", 0, err
		}
		if response.Result == nil || response.Result.Status == nil {
			return "", 0, fmt.Errorf("invalid Feishu export result")
		}
		r := response.Result
		switch *r.Status {
		case 0:
			if !validID(r.Token) || r.Size == nil || *r.Size < 0 {
				return "", 0, fmt.Errorf("invalid Feishu export file metadata")
			}
			return r.Token, *r.Size, nil
		case 1, 2:
			if err := wait(ctx, time.Second); err != nil {
				return "", 0, err
			}
		default:
			return "", 0, fmt.Errorf("Feishu document export failed (status %d); check document export permission and format", *r.Status)
		}
	}
}

func fileDownloadEndpoint(id string) string {
	return "/drive/v1/files/" + id + "/download"
}

func (d *Feishu) Link(ctx context.Context, file model.Obj, _ model.LinkArgs) (*model.Link, error) {
	s, err := d.current()
	if err != nil {
		return nil, err
	}
	o, err := unwrap(file)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.operationContext(ctx, 2*time.Minute)
	defer cancel()
	var endpoint string
	var size int64
	if o.Kind == "file" {
		endpoint = fileDownloadEndpoint(o.ID)
		size, err = s.probeSize(ctx, endpoint)
	} else if s.extension(o.Kind) != "" {
		var token string
		token, size, err = s.export(ctx, o)
		endpoint = "/drive/v1/export_tasks/file/" + token + "/download"
	} else {
		return nil, errs.NotSupport
	}
	if err != nil {
		return nil, err
	}
	ttl := time.Duration(0) // Resolve authorization and exports again for each download.
	return &model.Link{RangeReader: &downloadReader{s: s, endpoint: endpoint, size: size}, ContentLength: size, Expiration: &ttl}, nil
}

func (s *session) openDownload(ctx context.Context, endpoint string, r http_range.Range) (*http.Response, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	token, err := s.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot construct Feishu download request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept-Encoding", "identity")
	if r.Start > 0 || r.Length >= 0 {
		value := "bytes=" + strconv.FormatInt(r.Start, 10) + "-"
		if r.Length > 0 {
			value += strconv.FormatInt(r.Start+r.Length-1, 10)
		}
		req.Header.Set("Range", value)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("Feishu download connection failed")
	}
	// Never follow a signed download location with application credentials.
	// A redirect is deliberately rejected until its contract is explicitly supported.
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		resp.Body.Close()
		return nil, &apiError{Status: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusOK && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		// API failures can be returned with HTTP 200. A real JSON file is normally
		// delivered as an attachment; keep that legitimate download supported.
		if resp.Header.Get("Content-Disposition") == "" {
			err := readJSON(resp, nil)
			if err == nil {
				err = fmt.Errorf("Feishu returned JSON instead of a file")
			}
			return nil, err
		}
	}
	return resp, nil
}

func parseContentRange(raw string) (start, end, total int64, err error) {
	if !strings.HasPrefix(raw, "bytes ") {
		return 0, 0, 0, fmt.Errorf("invalid Feishu Content-Range")
	}
	rangePart, totalPart, ok := strings.Cut(strings.TrimPrefix(raw, "bytes "), "/")
	if !ok {
		return 0, 0, 0, fmt.Errorf("invalid Feishu Content-Range")
	}
	startPart, endPart, ok := strings.Cut(rangePart, "-")
	if !ok {
		return 0, 0, 0, fmt.Errorf("invalid Feishu Content-Range")
	}
	start, e1 := strconv.ParseInt(startPart, 10, 64)
	end, e2 := strconv.ParseInt(endPart, 10, 64)
	total, e3 := strconv.ParseInt(totalPart, 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || start < 0 || end < start || total <= end {
		return 0, 0, 0, fmt.Errorf("invalid Feishu Content-Range")
	}
	return start, end, total, nil
}

func (s *session) probeSize(ctx context.Context, endpoint string) (int64, error) {
	resp, err := s.openDownload(ctx, endpoint, http_range.Range{Length: 1})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 206 {
		start, end, total, err := parseContentRange(resp.Header.Get("Content-Range"))
		if err != nil || start != 0 || end != 0 {
			return 0, fmt.Errorf("invalid Feishu size probe response")
		}
		return total, nil
	}
	if resp.ContentLength < 0 {
		return 0, fmt.Errorf("Feishu download did not provide a file size")
	}
	return resp.ContentLength, nil
}

type downloadReader struct {
	s        *session
	endpoint string
	size     int64
}
type downloadBody struct {
	io.Reader
	body   io.Closer
	cancel context.CancelFunc
}

func (b *downloadBody) Close() error { b.cancel(); return b.body.Close() }

func (r *downloadReader) RangeRead(ctx context.Context, requested http_range.Range) (io.ReadCloser, error) {
	if requested.Start < 0 || requested.Length < -1 || requested.Start > r.size {
		return nil, fmt.Errorf("invalid download range")
	}
	if requested.Length == 0 {
		return io.NopCloser(strings.NewReader("")), nil
	}
	length := r.size - requested.Start
	if requested.Length >= 0 && requested.Length < length {
		length = requested.Length
	}
	if length == 0 {
		return io.NopCloser(strings.NewReader("")), nil
	}
	ctx, cancel := r.s.operationContext(ctx, 48*time.Hour)
	resp, err := r.s.openDownload(ctx, r.endpoint, http_range.Range{Start: requested.Start, Length: length})
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode == 206 {
		start, end, total, err := parseContentRange(resp.Header.Get("Content-Range"))
		if err != nil || start != requested.Start || end != requested.Start+length-1 || total != r.size {
			resp.Body.Close()
			cancel()
			return nil, fmt.Errorf("Feishu returned a mismatched download range")
		}
	} else {
		if resp.ContentLength >= 0 && resp.ContentLength != r.size {
			resp.Body.Close()
			cancel()
			return nil, fmt.Errorf("Feishu file changed; refresh the directory and retry")
		}
		// Correctly emulate a range if the upstream ignores Range. Never label
		// the full file as a partial response without skipping the requested offset.
		if _, err := io.CopyN(io.Discard, resp.Body, requested.Start); err != nil {
			resp.Body.Close()
			cancel()
			return nil, err
		}
	}
	return &downloadBody{Reader: io.LimitReader(resp.Body, length), body: resp.Body, cancel: cancel}, nil
}
