package zip

import (
	"bytes"
	"io"
	"io/fs"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/archive/tool"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/yeka/zip"
	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/transform"
)

type WrapReader struct {
	Reader *zip.Reader
	efs    bool
}

func (r *WrapReader) Files() []tool.SubFile {
	ret := make([]tool.SubFile, 0, len(r.Reader.File))
	for _, f := range r.Reader.File {
		ret = append(ret, &WrapFile{f: f, efs: r.efs})
	}
	return ret
}

type WrapFileInfo struct {
	fs.FileInfo
	efs bool
}

func (f *WrapFileInfo) Name() string {
	return decodeName(f.FileInfo.Name(), f.efs)
}

type WrapFile struct {
	f   *zip.File
	efs bool
}

func (f *WrapFile) Name() string {
	return decodeName(f.f.Name, f.efs)
}

func (f *WrapFile) FileInfo() fs.FileInfo {
	return &WrapFileInfo{FileInfo: f.f.FileInfo(), efs: f.efs}
}

func (f *WrapFile) Open() (io.ReadCloser, error) {
	return f.f.Open()
}

func (f *WrapFile) IsEncrypted() bool {
	return f.f.IsEncrypted()
}

func (f *WrapFile) SetPassword(password string) {
	f.f.SetPassword(password)
}

func (z *Zip) getReader(ss []*stream.SeekableStream) (r *zip.Reader, efs bool, err error) {
	if len(ss) > 1 && z.traditionalSecondPartRegExp.MatchString(ss[1].GetName()) {
		ss = append(ss[1:], ss[0])
	}
	reader, err := stream.NewMultiReaderAt(ss)
	if err != nil {
		return nil, false, err
	}
	buf := make([]byte, 8)
	n, err := reader.ReadAt(buf, 0)
	efs = err == nil && n == 8 && (buf[7]&0x08) > 0
	r, err = zip.NewReader(reader, reader.Size())
	return
}

func filterPassword(err error) error {
	if err != nil && strings.Contains(err.Error(), "password") {
		return errs.WrongArchivePassword
	}
	return err
}

func decodeName(name string, efs bool) string {
	if efs {
		return name
	}
	enc, err := ianaindex.IANA.Encoding(setting.GetStr(conf.NonEFSZipEncoding))
	if err != nil {
		return name
	}
	i := bytes.NewReader([]byte(name))
	decoder := transform.NewReader(i, enc.NewDecoder())
	content, _ := io.ReadAll(decoder)
	return string(content)
}
