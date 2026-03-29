package zip

import (
	"bytes"
	"io"
	"io/fs"
	"strings"

	"github.com/KirCute/zip"
	"github.com/OpenListTeam/OpenList/v4/internal/archive/tool"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/transform"
)

type WrapReader struct {
	Reader *zip.Reader
}

func (r *WrapReader) Files() []tool.SubFile {
	ret := make([]tool.SubFile, 0, len(r.Reader.File))
	for _, f := range r.Reader.File {
		ret = append(ret, &WrapFile{f: f})
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
	f *zip.File
}

func (f *WrapFile) Name() string {
	return decodeName(f.f.Name, isEFS(f.f.Flags))
}

func (f *WrapFile) FileInfo() fs.FileInfo {
	return &WrapFileInfo{FileInfo: f.f.FileInfo(), efs: isEFS(f.f.Flags)}
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

func makePart(ss *stream.SeekableStream) (zip.SizeReaderAt, error) {
	ra, err := stream.NewReadAtSeeker(ss, 0)
	if err != nil {
		return nil, err
	}
	return &inlineSizeReaderAt{ReaderAt: ra, size: ss.GetSize()}, nil
}

func (z *Zip) getReader(ss []*stream.SeekableStream) (*zip.Reader, error) {
	if len(ss) > 1 && z.traditionalSecondPartRegExp.MatchString(ss[1].GetName()) {
		ss = append(ss[1:], ss[0])
		ras := make([]zip.SizeReaderAt, 0, len(ss))
		for _, s := range ss {
			ra, err := makePart(s)
			if err != nil {
				return nil, err
			}
			ras = append(ras, ra)
		}
		return zip.NewMultipartReader(ras)
	} else {
		reader, err := stream.NewMultiReaderAt(ss)
		if err != nil {
			return nil, err
		}
		return zip.NewReader(reader, reader.Size())
	}
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

func isEFS(flags uint16) bool {
	return (flags & 0x800) > 0
}

type inlineSizeReaderAt struct {
	io.ReaderAt
	size int64
}

func (i *inlineSizeReaderAt) Size() int64 {
	return i.size
}
