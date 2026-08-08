package utils

import (
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	log "github.com/sirupsen/logrus"
)

// CopyFile File copies a single file from src to dst
func CopyFile(src, dst string) error {
	var err error
	var srcfd *os.File
	var dstfd *os.File
	var srcinfo os.FileInfo

	if srcfd, err = os.Open(src); err != nil {
		return err
	}
	defer srcfd.Close()

	if dstfd, err = CreateNestedFile(dst); err != nil {
		return err
	}
	defer dstfd.Close()

	if _, err = CopyWithBuffer(dstfd, srcfd); err != nil {
		return err
	}
	if srcinfo, err = os.Stat(src); err != nil {
		return err
	}
	return os.Chmod(dst, srcinfo.Mode())
}

// CopyDir Dir copies a whole directory recursively
func CopyDir(src, dst string) error {
	var err error
	var fds []os.DirEntry
	var srcinfo os.FileInfo

	if srcinfo, err = os.Stat(src); err != nil {
		return err
	}
	if err = os.MkdirAll(dst, srcinfo.Mode()); err != nil {
		return err
	}
	if fds, err = os.ReadDir(src); err != nil {
		return err
	}
	for _, fd := range fds {
		srcfp := path.Join(src, fd.Name())
		dstfp := path.Join(dst, fd.Name())

		if fd.IsDir() {
			if err = CopyDir(srcfp, dstfp); err != nil {
				fmt.Println(err)
			}
		} else {
			if err = CopyFile(srcfp, dstfp); err != nil {
				fmt.Println(err)
			}
		}
	}
	return nil
}

// SymlinkOrCopyFile symlinks a file or copy if symlink failed
func SymlinkOrCopyFile(src, dst string) error {
	if err := CreateNestedDirectory(filepath.Dir(dst)); err != nil {
		return err
	}
	if err := os.Symlink(src, dst); err != nil {
		return CopyFile(src, dst)
	}
	return nil
}

// Exists determine whether the file exists
func Exists(name string) bool {
	if _, err := os.Stat(name); err != nil {
		if os.IsNotExist(err) {
			return false
		}
	}
	return true
}

// CreateNestedDirectory create nested directory
func CreateNestedDirectory(path string) error {
	err := os.MkdirAll(path, 0700)
	if err != nil {
		log.Errorf("can't create folder, %s", err)
	}
	return err
}

// CreateNestedFile create nested file
func CreateNestedFile(path string) (*os.File, error) {
	basePath := filepath.Dir(path)
	if err := CreateNestedDirectory(basePath); err != nil {
		return nil, err
	}
	return os.Create(path)
}

// CreateTempFile create temp file from io.ReadCloser, and seek to 0
func CreateTempFile(r io.Reader, size int64) (*os.File, error) {
	if f, ok := r.(*os.File); ok {
		return f, nil
	}
	f, err := os.CreateTemp(conf.Conf.TempDir, "file-*")
	if err != nil {
		return nil, err
	}
	readBytes, err := CopyWithBuffer(f, r)
	if err != nil {
		_ = os.Remove(f.Name())
		return nil, errs.NewErr(err, "CreateTempFile failed")
	}
	if size > 0 && readBytes != size {
		_ = os.Remove(f.Name())
		return nil, errs.NewErr(err, "CreateTempFile failed, incoming stream actual size= %d, expect = %d ", readBytes, size)
	}
	_, err = f.Seek(0, io.SeekStart)
	if err != nil {
		_ = os.Remove(f.Name())
		return nil, errs.NewErr(err, "CreateTempFile failed, can't seek to 0 ")
	}
	return f, nil
}

// GetFileType get file type
func GetFileType(filename string) int {
	ext := strings.ToLower(Ext(filename))
	if SliceContains(conf.SlicesMap[conf.AudioTypes], ext) {
		return conf.AUDIO
	}
	if SliceContains(conf.SlicesMap[conf.VideoTypes], ext) {
		return conf.VIDEO
	}
	if SliceContains(conf.SlicesMap[conf.ImageTypes], ext) {
		return conf.IMAGE
	}
	if SliceContains(conf.SlicesMap[conf.TextTypes], ext) {
		return conf.TEXT
	}
	return conf.UNKNOWN
}

func GetObjType(filename string, isDir bool) int {
	if isDir {
		return conf.FOLDER
	}
	return GetFileType(filename)
}

var extraMimeTypes = map[string]string{
	".apk": "application/vnd.android.package-archive",
}

func GetMimeType(name string) string {
	ext := path.Ext(name)
	if m, ok := extraMimeTypes[ext]; ok {
		return m
	}
	m := mime.TypeByExtension(ext)
	if m != "" {
		return m
	}
	return "application/octet-stream"
}

const (
	KB = 1 << (10 * (iota + 1))
	MB
	GB
	TB
)

// IsSystemFile checks if a filename is a common system file that should be ignored
// Returns true for files like .DS_Store, desktop.ini, Thumbs.db, and Apple Double files (._*)
func IsSystemFile(filename string) bool {
	// Common system files
	switch filename {
	case ".DS_Store", "desktop.ini", "Thumbs.db", "@eaDir":
		return true
	}

	// Apple Double files (._*)
	if strings.HasPrefix(filename, "._") {
		return true
	}

	return false
}

// ParseSize parses human-readable file size strings like "100MB", "10KB", "10G", "1.5GB", "500B", "500" into bytes.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	s = strings.ToUpper(s)

	i := 0
	for i < len(s) && ((s[i] >= '0' && s[i] <= '9') || s[i] == '.') {
		i++
	}
	numStr := strings.TrimSpace(s[:i])
	unitStr := strings.TrimSpace(s[i:])

	if numStr == "" {
		return 0, fmt.Errorf("invalid size format: %s", s)
	}

	val, err := strconv.ParseFloat(numStr, 64)
	if err != nil || val < 0 {
		return 0, fmt.Errorf("invalid size value: %s", numStr)
	}

	var multiplier float64 = 1
	switch unitStr {
	case "", "B", "BYTE", "BYTES":
		multiplier = 1
	case "K", "KB", "KIB":
		multiplier = float64(KB)
	case "M", "MB", "MIB":
		multiplier = float64(MB)
	case "G", "GB", "GIB":
		multiplier = float64(GB)
	case "T", "TB", "TIB":
		multiplier = float64(TB)
	case "P", "PB", "PIB":
		multiplier = float64(1 << 50)
	default:
		return 0, fmt.Errorf("unknown unit in size format: %s", unitStr)
	}

	return int64(val * multiplier), nil
}
