package zip

import (
	"io"
	"regexp"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/archive/tool"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
)

type Zip struct {
	traditionalSecondPartRegExp *regexp.Regexp
}

func (z *Zip) AcceptedExtensions() []string {
	return []string{".livp"}
}

func (z *Zip) AcceptedMultipartExtensions() map[string]tool.MultipartExtension {
	return map[string]tool.MultipartExtension{
		".zip":     {PartFileFormat: regexp.MustCompile(`^.*\.z(\d+)$`), SecondPartIndex: 1},
		".zip.001": {PartFileFormat: regexp.MustCompile(`^.*\.zip\.(\d+)$`), SecondPartIndex: 2},
	}
}

func (z *Zip) GetMeta(ss []*stream.SeekableStream, args model.ArchiveArgs) (model.ArchiveMeta, error) {
	zipReader, err := z.getReader(ss)
	if err != nil {
		return nil, err
	}
	efs := true
	if len(zipReader.File) > 0 {
		efs = isEFS(zipReader.File[0].Flags)
	}
	encrypted, tree := tool.GenerateMetaTreeFromFolderTraversal(&WrapReader{Reader: zipReader})
	return &model.ArchiveMetaInfo{
		Comment:   decodeName(zipReader.Comment, efs),
		Encrypted: encrypted,
		Tree:      tree,
	}, nil
}

func (z *Zip) List(ss []*stream.SeekableStream, args model.ArchiveInnerArgs) ([]model.Obj, error) {
	zipReader, err := z.getReader(ss)
	if err != nil {
		return nil, err
	}
	innerPath := strings.Trim(strings.TrimPrefix(args.InnerPath, "/"), "/")
	prefix := ""
	if innerPath != "" && innerPath != "." {
		prefix = innerPath + "/"
	}
	ret := make([]model.Obj, 0)
	seenDir := make(map[string]bool)
	passVerified := false
	for _, file := range zipReader.File {
		name := strings.TrimSuffix(decodeName(file.Name, isEFS(file.Flags)), "/")
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, prefix)
		if rest == "" {
			continue
		}
		if !passVerified && file.IsEncrypted() {
			file.SetPassword(args.Password)
			rc, e := file.Open()
			if e != nil {
				return nil, filterPassword(e)
			}
			_ = rc.Close()
			passVerified = true
		}
		// 压缩包可能只记录文件路径，不记录目录条目：把更深层的路径
		// 折叠成当前层的合成目录，避免子目录在预览里整体消失。
		if idx := strings.Index(rest, "/"); idx >= 0 {
			dirName := rest[:idx]
			if !seenDir[dirName] {
				seenDir[dirName] = true
				ret = append(ret, &model.Object{
					Name:     dirName,
					Modified: ss[0].ModTime(),
					IsFolder: true,
				})
			}
			continue
		}
		ret = append(ret, tool.MakeModelObj(&WrapFileInfo{FileInfo: file.FileInfo(), efs: isEFS(file.Flags)}))
	}
	if len(ret) == 0 {
		return nil, errs.ObjectNotFound
	}
	return ret, nil
}

func (z *Zip) Extract(ss []*stream.SeekableStream, args model.ArchiveInnerArgs) (io.ReadCloser, int64, error) {
	zipReader, err := z.getReader(ss)
	if err != nil {
		return nil, 0, err
	}
	innerPath := strings.TrimPrefix(args.InnerPath, "/")
	for _, file := range zipReader.File {
		if decodeName(file.Name, isEFS(file.Flags)) == innerPath {
			if file.IsEncrypted() {
				file.SetPassword(args.Password)
			}
			r, e := file.Open()
			if e != nil {
				return nil, 0, e
			}
			return r, file.FileInfo().Size(), nil
		}
	}
	return nil, 0, errs.ObjectNotFound
}

func (z *Zip) Decompress(ss []*stream.SeekableStream, outputPath string, args model.ArchiveInnerArgs, up model.UpdateProgress) error {
	zipReader, err := z.getReader(ss)
	if err != nil {
		return err
	}
	return tool.DecompressFromFolderTraversal(&WrapReader{Reader: zipReader}, outputPath, args, up)
}

var _ tool.Tool = (*Zip)(nil)

func init() {
	tool.RegisterTool(&Zip{
		traditionalSecondPartRegExp: regexp.MustCompile(`^.*\.z0*1$`),
	})
}
