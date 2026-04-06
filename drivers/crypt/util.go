package crypt

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	stdpath "path"
	"path/filepath"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/disintegration/imaging"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

const (
	thumbDirName = ".thumbnails"
	thumbExt     = ".webp"
	thumbWidth   = 144
)

// will give the best guessing based on the path
func guessPath(path string) (isFolder, secondTry bool) {
	if strings.HasSuffix(path, "/") {
		//confirmed a folder
		return true, false
	}
	lastSlash := strings.LastIndex(path, "/")
	if !strings.Contains(path[lastSlash:], ".") {
		//no dot, try folder then try file
		return true, true
	}
	return false, true
}

func (d *Crypt) encryptPath(path string, isFolder bool) string {
	if isFolder {
		return d.cipher.EncryptDirName(path)
	}
	dir, fileName := filepath.Split(path)
	return stdpath.Join(d.cipher.EncryptDirName(dir), d.cipher.EncryptFileName(fileName))
}

func isThumbPath(path string) bool {
	path = utils.FixAndCleanPath(path)
	return stdpath.Base(stdpath.Dir(path)) == thumbDirName && strings.HasSuffix(stdpath.Base(path), thumbExt)
}

func thumbSourcePath(path string) (string, bool) {
	path = utils.FixAndCleanPath(path)
	if !isThumbPath(path) {
		return "", false
	}
	name := strings.TrimSuffix(stdpath.Base(path), thumbExt)
	if name == "" {
		return "", false
	}
	parentDir := stdpath.Dir(stdpath.Dir(path))
	return stdpath.Join(parentDir, name), true
}

func thumbTargetDir(path string) string {
	return stdpath.Dir(utils.FixAndCleanPath(path))
}

func (d *Crypt) newThumbObject(path string, sourceObj model.Obj) *thumbObject {
	path = utils.FixAndCleanPath(path)
	return &thumbObject{
		Object: model.Object{
			Path:     stdpath.Join(d.RemotePath, d.encryptPath(path, false)),
			Name:     stdpath.Base(path),
			Modified: sourceObj.ModTime(),
			Ctime:    sourceObj.CreateTime(),
		},
		thumbPath: path,
		sourceObj: sourceObj,
	}
}

func (d *Crypt) ensureThumb(ctx context.Context, thumb *thumbObject) error {
	if _, err := d.getActual(ctx, thumb.thumbPath); err == nil {
		return nil
	}
	_, err, _ := d.thumbGroup.Do(thumb.thumbPath, func() (struct{}, error) {
		if _, err := d.getActual(ctx, thumb.thumbPath); err == nil {
			return struct{}{}, nil
		}
		buf, err := d.buildThumb(ctx, thumb.sourceObj)
		if err != nil {
			return struct{}{}, err
		}
		file := &stream.FileStream{
			Obj: &model.Object{
				Name:     stdpath.Base(thumb.thumbPath),
				Size:     int64(buf.Len()),
				Modified: thumb.sourceObj.ModTime(),
			},
			Reader:   bytes.NewReader(buf.Bytes()),
			Mimetype: "image/webp",
		}
		if err := op.Put(ctx, d, thumbTargetDir(thumb.thumbPath), file, nil); err != nil {
			return struct{}{}, err
		}
		if _, err := d.getActual(ctx, thumb.thumbPath); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	return err
}

func (d *Crypt) buildThumb(ctx context.Context, sourceObj model.Obj) (*bytes.Buffer, error) {
	sourceFile, err := d.openSourceTempFile(ctx, sourceObj)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = sourceFile.Close()
		_ = os.Remove(sourceFile.Name())
	}()

	image, err := imaging.Decode(sourceFile, imaging.AutoOrientation(true))
	if err != nil {
		return nil, err
	}
	thumbImg := imaging.Resize(image, thumbWidth, 0, imaging.Lanczos)

	tmpPNG, err := os.CreateTemp(conf.Conf.TempDir, "crypt-thumb-*.png")
	if err != nil {
		return nil, err
	}
	tmpPNGPath := tmpPNG.Name()
	defer func() {
		_ = tmpPNG.Close()
		_ = os.Remove(tmpPNGPath)
	}()
	if err := imaging.Encode(tmpPNG, thumbImg, imaging.PNG); err != nil {
		return nil, err
	}
	if err := tmpPNG.Close(); err != nil {
		return nil, err
	}

	buf := bytes.NewBuffer(nil)
	cmd := ffmpeg.Input(tmpPNGPath).
		Output("pipe:", ffmpeg.KwArgs{"vcodec": "libwebp", "f": "webp"}).
		GlobalArgs("-loglevel", "error").
		Silent(true).
		WithOutput(buf, io.Discard)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("encode webp failed: %w", err)
	}
	if buf.Len() == 0 {
		return nil, fmt.Errorf("encode webp failed: empty output")
	}
	return buf, nil
}

func (d *Crypt) openSourceTempFile(ctx context.Context, sourceObj model.Obj) (*os.File, error) {
	link, err := d.Link(ctx, sourceObj, model.LinkArgs{})
	if err != nil {
		return nil, err
	}
	defer link.Close()

	reader, err := link.RangeReader.RangeRead(ctx, http_range.Range{Length: -1})
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return utils.CreateTempFile(reader, sourceObj.GetSize())
}
