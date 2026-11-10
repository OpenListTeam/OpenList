package feishu

import (
	"bytes"
	"context"
	"fmt"
	"hash/adler32"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// Multipart bodies contain at most one bounded block. Large files are streamed
// sequentially and never buffered in full or retried as non-idempotent mutations.
func (s *session) multipart(ctx context.Context, endpoint string, fields map[string]string, name string, data []byte, up func(float64), dst any) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return err
		}
	}
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		return err
	}
	if _, err = part.Write(data); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var reader io.Reader = &body
	if up != nil {
		reader = &uploadProgress{reader: &body, total: int64(body.Len()), up: up}
	}
	reader = driver.NewLimitedUploadStream(ctx, reader)
	resp, err := s.request(ctx, http.MethodPost, endpoint, reader, writer.FormDataContentType(), true, int64(body.Len()))
	if err != nil {
		return err
	}
	return readJSON(resp, dst)
}

type uploadProgress struct {
	reader      io.Reader
	total, sent int64
	up          func(float64)
}

func (p *uploadProgress) Read(b []byte) (int, error) {
	n, err := p.reader.Read(b)
	p.sent += int64(n)
	p.up(float64(p.sent) / float64(p.total) * 99)
	return n, err
}

func (d *Feishu) Put(ctx context.Context, parent model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	s, err := d.current()
	if err != nil {
		return nil, err
	}
	if s.cfg.ReadOnly {
		return nil, fmt.Errorf("Feishu storage is read-only")
	}
	dir, err := unwrap(parent)
	if err != nil {
		return nil, err
	}
	if dir.Kind != "folder" {
		return nil, errs.NotSupport
	}
	if !validName(file.GetName()) {
		return nil, fmt.Errorf("invalid Feishu file name")
	}
	if file.GetSize() <= 0 {
		return nil, fmt.Errorf("Feishu requires a non-empty file with a known size")
	}
	if file.GetExist() != nil {
		return nil, fmt.Errorf("Feishu overwrite upload is not supported")
	}
	ctx, cancel := s.operationContext(ctx, 48*time.Hour)
	defer cancel()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.checkConflict(ctx, dir.ID, file.GetName()); err != nil {
		return nil, err
	}
	reader := &driver.ReaderWithCtx{Ctx: ctx, Reader: file}
	var result struct {
		Token string `json:"file_token"`
	}
	if file.GetSize() <= 20<<20 {
		data, err := io.ReadAll(io.LimitReader(reader, file.GetSize()+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) != file.GetSize() {
			return nil, fmt.Errorf("Feishu upload stream size does not match declared size")
		}
		if err := s.multipart(ctx, "/drive/v1/files/upload_all", map[string]string{"file_name": file.GetName(), "parent_type": "explorer", "parent_node": dir.ID, "size": strconv.FormatInt(file.GetSize(), 10)}, file.GetName(), data, up, &result); err != nil {
			return nil, err
		}
	} else {
		var prepared struct {
			ID        string `json:"upload_id"`
			BlockSize int64  `json:"block_size"`
			BlockNum  int64  `json:"block_num"`
		}
		if err := s.json(ctx, http.MethodPost, "/drive/v1/files/upload_prepare", map[string]any{"file_name": file.GetName(), "parent_type": "explorer", "parent_node": dir.ID, "size": file.GetSize()}, &prepared); err != nil {
			return nil, err
		}
		if !validID(prepared.ID) || prepared.BlockSize <= 0 || prepared.BlockSize > 20<<20 || prepared.BlockNum != 1+(file.GetSize()-1)/prepared.BlockSize {
			return nil, fmt.Errorf("invalid Feishu upload block strategy")
		}
		buffer := make([]byte, int(prepared.BlockSize))
		var sent int64
		for seq := int64(0); seq < prepared.BlockNum; seq++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			count := min(prepared.BlockSize, file.GetSize()-sent)
			if _, err := io.ReadFull(reader, buffer[:count]); err != nil {
				return nil, err
			}
			data := buffer[:count]
			fields := map[string]string{"upload_id": prepared.ID, "seq": strconv.FormatInt(seq, 10), "size": strconv.FormatInt(count, 10), "checksum": strconv.FormatUint(uint64(adler32.Checksum(data)), 10)}
			var progress func(float64)
			if up != nil {
				base := sent
				progress = func(p float64) { up((float64(base) + float64(count)*p/100) / float64(file.GetSize()) * 99) }
			}
			if err := s.multipart(ctx, "/drive/v1/files/upload_part", fields, file.GetName(), data, progress, nil); err != nil {
				return nil, err
			}
			sent += count
			if up != nil {
				up(float64(sent) / float64(file.GetSize()) * 99)
			}
		}
		var extra [1]byte
		if n, err := reader.Read(extra[:]); n != 0 || err != io.EOF {
			return nil, fmt.Errorf("Feishu upload stream exceeds declared size or could not be completed")
		}
		if err := s.json(ctx, http.MethodPost, "/drive/v1/files/upload_finish", map[string]any{"upload_id": prepared.ID, "block_num": prepared.BlockNum}, &result); err != nil {
			return nil, err
		}
	}
	if !validID(result.Token) {
		return nil, fmt.Errorf("invalid Feishu uploaded file token")
	}
	if up != nil {
		up(100)
	}
	s.fileSizes.Store(result.Token, file.GetSize())
	return &object{Object: model.Object{ID: result.Token, Name: file.GetName(), Size: file.GetSize(), Modified: time.Now()}, Kind: "file", OriginalName: file.GetName()}, nil
}

var _ driver.PutResult = (*Feishu)(nil)
