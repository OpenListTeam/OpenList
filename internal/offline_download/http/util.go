package http

import (
	"fmt"
	"mime"
	"path/filepath"
	"strings"
)

func sanitizeFilename(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	sep := string(filepath.Separator)
	raw = strings.ReplaceAll(raw, "/", sep)
	raw = strings.ReplaceAll(raw, "\\", sep)
	filename := filepath.Base(filepath.Clean(raw))
	if filename == "." || filename == sep || !filepath.IsLocal(filename) {
		return "", fmt.Errorf("invalid filename: %q", raw)
	}
	return filename, nil
}

func parseFilenameFromContentDisposition(contentDisposition string) (string, error) {
	if contentDisposition == "" {
		return "", fmt.Errorf("Content-Disposition is empty")
	}
	_, params, err := mime.ParseMediaType(contentDisposition)
	if err != nil {
		return "", err
	}
	filename := params["filename"]
	if filename == "" {
		filename = params["filename*"]
	}
	if filename == "" {
		return "", fmt.Errorf("filename not found in Content-Disposition: [%s]", contentDisposition)
	}
	filename, err = sanitizeFilename(filename)
	if err != nil {
		return "", fmt.Errorf("invalid filename in Content-Disposition: [%s]", contentDisposition)
	}
	return filename, nil
}
