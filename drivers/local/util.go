package local

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/disintegration/imaging"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

func isSymlinkDir(f fs.FileInfo, path string) bool {
	if f.Mode()&os.ModeSymlink == os.ModeSymlink ||
		(runtime.GOOS == "windows" && f.Mode()&os.ModeIrregular == os.ModeIrregular) { // os.ModeIrregular is Junction bit in Windows
		dst, err := os.Readlink(filepath.Join(path, f.Name()))
		if err != nil {
			return false
		}
		if !filepath.IsAbs(dst) {
			dst = filepath.Join(path, dst)
		}
		stat, err := os.Stat(dst)
		if err != nil {
			return false
		}
		return stat.IsDir()
	}
	return false
}

// Get the snapshot of the video
func (d *Local) GetSnapshot(videoPath string) (imgData *bytes.Buffer, err error) {
	// Run ffprobe to get the video duration
	jsonOutput, err := ffmpeg.Probe(videoPath)
	if err != nil {
		return nil, err
	}
	// get format.duration from the json string
	type probeFormat struct {
		Duration string `json:"duration"`
	}
	type probeData struct {
		Format probeFormat `json:"format"`
	}
	var probe probeData
	err = json.Unmarshal([]byte(jsonOutput), &probe)
	if err != nil {
		return nil, err
	}
	totalDuration, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil {
		return nil, err
	}

	var ss string
	if d.videoThumbPosIsPercentage {
		ss = fmt.Sprintf("%f", totalDuration*d.videoThumbPos)
	} else {
		// If the value is greater than the total duration, use the total duration
		if d.videoThumbPos > totalDuration {
			ss = fmt.Sprintf("%f", totalDuration)
		} else {
			ss = fmt.Sprintf("%f", d.videoThumbPos)
		}
	}

	// Run ffmpeg to get the snapshot
	srcBuf := bytes.NewBuffer(nil)
	// If the remaining time from the seek point to the end of the video is less
	// than the duration of a single frame, ffmpeg cannot extract any frames
	// within the specified range and will exit with an error.
	// The "noaccurate_seek" option prevents this error and would also speed up
	// the seek process.
	stream := ffmpeg.Input(videoPath, ffmpeg.KwArgs{"ss": ss, "noaccurate_seek": ""}).
		Output("pipe:", ffmpeg.KwArgs{"vframes": 1, "format": "image2", "vcodec": "mjpeg"}).
		GlobalArgs("-loglevel", "error").Silent(true).
		WithOutput(srcBuf, os.Stdout)
	if err = stream.Run(); err != nil {
		return nil, err
	}
	return srcBuf, nil
}

func readDir(dirname string) ([]fs.FileInfo, error) {
	f, err := os.Open(dirname)
	if err != nil {
		return nil, err
	}
	list, err := f.Readdir(-1)
	f.Close()
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name() < list[j].Name() })
	return list, nil
}

func (d *Local) getThumb(file model.Obj) (*bytes.Buffer, *string, error) {
	fullPath := file.GetPath()
	thumbPrefix := "openlist_thumb_"
	thumbName := thumbPrefix + utils.GetMD5EncodeStr(fullPath) + ".png"
	if d.ThumbCacheFolder != "" {
		// skip if the file is a thumbnail
		if strings.HasPrefix(file.GetName(), thumbPrefix) {
			return nil, &fullPath, nil
		}
		thumbPath := filepath.Join(d.ThumbCacheFolder, thumbName)
		if utils.Exists(thumbPath) {
			return nil, &thumbPath, nil
		}
	}
	var srcBuf *bytes.Buffer
	if utils.GetFileType(file.GetName()) == conf.VIDEO {
		videoBuf, err := d.GetSnapshot(fullPath)
		if err != nil {
			return nil, nil, err
		}
		srcBuf = videoBuf
	} else {
		imgData, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, nil, err
		}
		imgBuf := bytes.NewBuffer(imgData)
		srcBuf = imgBuf
	}

	image, err := imaging.Decode(srcBuf, imaging.AutoOrientation(true))
	if err != nil {
		return nil, nil, err
	}
	thumbImg := imaging.Resize(image, 144, 0, imaging.Lanczos)
	var buf bytes.Buffer
	err = imaging.Encode(&buf, thumbImg, imaging.PNG)
	if err != nil {
		return nil, nil, err
	}
	if d.ThumbCacheFolder != "" {
		err = os.WriteFile(filepath.Join(d.ThumbCacheFolder, thumbName), buf.Bytes(), 0666)
		if err != nil {
			return nil, nil, err
		}
	}
	return &buf, nil, nil
}

type DirectoryMap struct {
	root string
	data sync.Map
}

type DirectoryNode struct {
	fileSum      int64
	directorySum int64
	children     []string
}

type DirectoryTask struct {
	path  string
	cache *DirectoryTaskCache
}

type DirectoryTaskCache struct {
	fileSum  int64
	children []string
}

func (m *DirectoryMap) Has(path string) bool {
	_, ok := m.data.Load(path)

	return ok
}

func (m *DirectoryMap) Get(path string) (*DirectoryNode, bool) {
	value, ok := m.data.Load(path)
	if !ok {
		return &DirectoryNode{}, false
	}

	node, ok := value.(*DirectoryNode)
	if !ok {
		return &DirectoryNode{}, false
	}

	return node, true
}

func (m *DirectoryMap) Set(path string, node *DirectoryNode) {
	m.data.Store(path, node)
}

func (m *DirectoryMap) Delete(path string) {
	m.data.Delete(path)
}

func (m *DirectoryMap) Clear() {
	m.data.Clear()
}

func (m *DirectoryMap) RecalculateDirSize() error {
	m.Clear()
	if m.root == "" {
		return fmt.Errorf("root path is not set")
	}

	size, err := m.CalculateDirSize(m.root)
	if err != nil {
		return err
	}

	if node, ok := m.Get(m.root); ok {
		node.fileSum = size
		node.directorySum = size
	}

	return nil
}

func (m *DirectoryMap) CalculateDirSize(dirname string) (int64, error) {
	stack := []DirectoryTask{
		{path: dirname},
	}

	for len(stack) > 0 {
		task := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if task.cache != nil {
			directorySum := int64(0)

			for _, filename := range task.cache.children {
				child, ok := m.Get(filepath.Join(task.path, filename))
				if !ok {
					return 0, fmt.Errorf("child node not found")
				}
				directorySum += child.fileSum + child.directorySum
			}

			m.Set(task.path, &DirectoryNode{
				fileSum:      task.cache.fileSum,
				directorySum: directorySum,
				children:     task.cache.children,
			})

			continue
		}

		files, err := readDir(task.path)
		if err != nil {
			return 0, err
		}

		fileSum := int64(0)
		directorySum := int64(0)

		children := []string{}
		queue := []DirectoryTask{}

		for _, f := range files {
			fullpath := filepath.Join(task.path, f.Name())
			isFolder := f.IsDir() || isSymlinkDir(f, fullpath)

			if isFolder {
				if node, ok := m.Get(fullpath); ok {
					directorySum += node.fileSum + node.directorySum
				} else {
					queue = append(queue, DirectoryTask{
						path: fullpath,
					})
				}

				children = append(children, f.Name())
			} else {
				fileSum += f.Size()
			}
		}

		if len(queue) > 0 {
			stack = append(stack, DirectoryTask{
				path: task.path,
				cache: &DirectoryTaskCache{
					fileSum:  fileSum,
					children: children,
				},
			})

			stack = append(stack, queue...)

			continue
		}

		m.Set(task.path, &DirectoryNode{
			fileSum:      fileSum,
			directorySum: directorySum,
			children:     children,
		})
	}

	if node, ok := m.Get(dirname); ok {
		return node.fileSum + node.directorySum, nil
	}

	return 0, nil
}

func (m *DirectoryMap) UpdateDirSize(dirname string) (int64, error) {
	node, ok := m.Get(dirname)
	if !ok {
		return 0, fmt.Errorf("directory node not found")
	}

	files, err := readDir(dirname)
	if err != nil {
		return 0, err
	}
	fileSum := int64(0)
	directorySum := int64(0)

	children := []string{}

	for _, f := range files {
		fullpath := filepath.Join(dirname, f.Name())
		isFolder := f.IsDir() || isSymlinkDir(f, fullpath)

		if isFolder {
			if node, ok := m.Get(fullpath); ok {
				directorySum += node.fileSum + node.directorySum
			} else {
				value, err := m.CalculateDirSize(fullpath)
				if err != nil {
					return 0, err
				}
				directorySum += value
			}

			children = append(children, f.Name())
		} else {
			fileSum += f.Size()
		}
	}

	for _, c := range node.children {
		if !slices.Contains(children, c) {
			m.DeleteDirNode(filepath.Join(dirname, c))
		}
	}

	node.fileSum = fileSum
	node.directorySum = directorySum
	node.children = children

	return fileSum + directorySum, nil
}

func (m *DirectoryMap) UpdateDirParents(dirname string) error {
	parentPath := filepath.Dir(dirname)
	for parentPath != m.root && !strings.HasPrefix(m.root, parentPath) {
		if node, ok := m.Get(parentPath); ok {
			directorySum := int64(0)

			for _, c := range node.children {
				child, ok := m.Get(filepath.Join(parentPath, c))
				if !ok {
					return fmt.Errorf("child node not found")
				}
				directorySum += child.fileSum + child.directorySum
			}

			node.directorySum = directorySum
		}

		parentPath = filepath.Dir(parentPath)
	}

	return nil
}

func (m *DirectoryMap) DeleteDirNode(dirname string) error {
	stack := []string{dirname}

	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if node, ok := m.Get(current); ok {
			for _, filename := range node.children {
				stack = append(stack, filepath.Join(current, filename))
			}

			m.Delete(current)
		}
	}

	return nil
}
