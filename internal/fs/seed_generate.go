package fs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	stdpath "path"
	"slices"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/tache"
	"github.com/pkg/errors"
)

// MaxSeedGenerateSyncSize bounds synchronous seed generation (1GB). Larger
// requests are turned into an asynchronous task.
const MaxSeedGenerateSyncSize = 1 * 1024 * 1024 * 1024

// SeedGenerateNeedsAsync reports whether the given files must be generated
// asynchronously because they exceed the synchronous size limit. It resolves
// each path and sums the file sizes, returning the first error encountered.
func SeedGenerateNeedsAsync(ctx context.Context, user *model.User, paths []string) (bool, error) {
	var total int64
	for _, requestedPath := range paths {
		fullPath, err := user.JoinPath(requestedPath)
		if err != nil {
			return false, err
		}
		storage, actualPath, err := op.GetStorageAndActualPath(fullPath)
		if err != nil {
			return false, err
		}
		obj, err := op.Get(ctx, storage, actualPath)
		if err != nil {
			return false, fmt.Errorf("seed path must be a readable file: %s", requestedPath)
		}
		if obj.IsDir() {
			return false, fmt.Errorf("seed path must be a readable file: %s", requestedPath)
		}
		total += obj.GetSize()
		if total > MaxSeedGenerateSyncSize {
			return true, nil
		}
	}
	return false, nil
}

// SeedHashSelection controls whole-file and piece hash inclusion.
type SeedHashSelection struct {
	Whole  bool `json:"whole"`
	Pieces bool `json:"pieces"`
}

// SeedHashMatrix controls the optional hash metadata stored in a seed.
type SeedHashMatrix struct {
	MD5    SeedHashSelection `json:"md5"`
	SHA1   SeedHashSelection `json:"sha1"`
	SHA256 SeedHashSelection `json:"sha256"`
}

// SeedGenerateParams carries a fully-resolved seed generation request, free of
// any HTTP transport concerns so it can run synchronously or as a task.
type SeedGenerateParams struct {
	Paths               []string
	Formats             []string
	Name                string
	Comment             string
	FileComments        map[string]string
	HashMatrix          SeedHashMatrix
	PieceSize           int64
	Trackers            []string
	Channels            []torrent.SeedChannel
	OutputPath          string
	IncludeShare        bool
	IncludeDirectSource bool
	ShareFiles          []string
	DirectFiles         []string
}

// SeedArtifact is one generated seed container.
type SeedArtifact struct {
	Format   string `json:"format"`
	Name     string `json:"name"`
	FileName string `json:"file_name"`
	SeedData string `json:"seed_data"`
	Size     int    `json:"size"`
	Path     string `json:"path,omitempty"`
}

// DeriveSeedName derives a sensible default seed name from the source paths:
// single selection uses the file name, multi selection uses the common base
// name (ignoring extensions) when all files share one, otherwise the folder name.
func DeriveSeedName(paths []string) string {
	if len(paths) == 0 {
		return "OpenList Seed"
	}
	if len(paths) == 1 {
		return seedBaseName(paths[0])
	}
	// Common base name ignoring extensions (e.g. a.docx + a.exe -> "a").
	commonBase := seedBaseName(paths[0])
	for _, p := range paths[1:] {
		if base := seedBaseName(p); base != commonBase {
			commonBase = ""
			break
		}
	}
	if commonBase != "" {
		return commonBase
	}
	// Fall back to the common parent directory name.
	dir := commonParentDir(paths)
	if base := stdpath.Base(dir); base != "" && base != "/" && base != "." {
		return base
	}
	return "OpenList Seed"
}

// seedBaseName returns the file name without its extension.
func seedBaseName(p string) string {
	base := stdpath.Base(p)
	return strings.TrimSuffix(base, stdpath.Ext(base))
}

// commonParentDir returns the longest common parent directory of the given paths.
func commonParentDir(paths []string) string {
	if len(paths) == 0 {
		return "/"
	}
	parts := strings.Split(strings.Trim(stdpath.Dir(paths[0]), "/"), "/")
	for _, p := range paths[1:] {
		cur := strings.Split(strings.Trim(stdpath.Dir(p), "/"), "/")
		n := 0
		for n < len(parts) && n < len(cur) && parts[n] == cur[n] {
			n++
		}
		parts = parts[:n]
	}
	if len(parts) == 0 {
		return "/"
	}
	return "/" + strings.Join(parts, "/")
}

// NormalizeSeedFormats validates and deduplicates a list of seed format names.
func NormalizeSeedFormats(rawFormats []string) ([]string, error) {
	formats := append([]string(nil), rawFormats...)
	if len(formats) == 1 && strings.TrimSpace(formats[0]) == "" {
		formats[0] = setting.GetStr(conf.SeedDefaultFormat, "oss")
	}
	if len(formats) > 3 {
		return nil, fmt.Errorf("at most three seed formats may be generated")
	}
	result := make([]string, 0, len(formats))
	seen := make(map[string]struct{}, len(formats))
	for _, rawFormat := range formats {
		format := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(rawFormat), "."))
		if format == "bt" {
			format = "torrent"
		}
		if format != "oss" && format != "torrent" && format != "cas" {
			return nil, fmt.Errorf("unsupported seed format %q", rawFormat)
		}
		if _, exists := seen[format]; exists {
			continue
		}
		seen[format] = struct{}{}
		result = append(result, format)
	}
	return result, nil
}

func seedFormats(params SeedGenerateParams) ([]string, error) {
	formats, err := NormalizeSeedFormats(params.Formats)
	if err != nil {
		return nil, err
	}
	if len(formats) == 0 {
		formats = []string{setting.GetStr(conf.SeedDefaultFormat, "oss")}
	}
	return formats, nil
}

func seedMatrixEmpty(matrix SeedHashMatrix) bool {
	return !matrix.MD5.Whole && !matrix.MD5.Pieces && !matrix.SHA1.Whole && !matrix.SHA1.Pieces && !matrix.SHA256.Whole && !matrix.SHA256.Pieces
}

func loadSeedDefaultMatrix() SeedHashMatrix {
	var matrix SeedHashMatrix
	if raw := strings.TrimSpace(setting.GetStr(conf.SeedDefaultMatrix)); raw != "" {
		_ = json.Unmarshal([]byte(raw), &matrix)
	}
	return matrix
}

func normalizedSeedMatrix(matrix SeedHashMatrix, formats []string) SeedHashMatrix {
	if seedMatrixEmpty(matrix) {
		matrix = loadSeedDefaultMatrix()
	}
	if seedMatrixEmpty(matrix) {
		matrix = SeedHashMatrix{
			MD5: SeedHashSelection{Whole: true, Pieces: true}, SHA1: SeedHashSelection{Whole: true, Pieces: true},
			SHA256: SeedHashSelection{Whole: true, Pieces: true},
		}
	}
	for _, format := range formats {
		switch format {
		case "torrent":
			matrix.SHA1 = SeedHashSelection{Whole: true, Pieces: true}
		case "cas":
			matrix.MD5 = SeedHashSelection{Whole: true, Pieces: true}
		}
	}
	return matrix
}

func canReuseListedHashes(hashInfo utils.HashInfo, matrix SeedHashMatrix) bool {
	if matrix.MD5.Pieces || matrix.SHA1.Pieces || matrix.SHA256.Pieces {
		return false
	}
	if matrix.MD5.Whole && hashInfo.GetHash(utils.MD5) == "" {
		return false
	}
	if matrix.SHA1.Whole && hashInfo.GetHash(utils.SHA1) == "" {
		return false
	}
	if matrix.SHA256.Whole && hashInfo.GetHash(utils.SHA256) == "" {
		return false
	}
	return true
}

func applySeedMatrix(file *torrent.SeedFile, matrix SeedHashMatrix) {
	if !matrix.MD5.Whole {
		file.Hashes.MD5 = ""
	}
	if !matrix.SHA1.Whole {
		file.Hashes.SHA1 = ""
	}
	if !matrix.SHA256.Whole {
		file.Hashes.SHA256 = ""
	}
	if file.Hashes.Pieces == nil {
		return
	}
	if !matrix.MD5.Pieces {
		file.Hashes.Pieces.MD5 = nil
	}
	if !matrix.SHA1.Pieces {
		file.Hashes.Pieces.SHA1 = nil
	}
	if !matrix.SHA256.Pieces {
		file.Hashes.Pieces.SHA256 = nil
	}
	if len(file.Hashes.Pieces.MD5) == 0 && len(file.Hashes.Pieces.SHA1) == 0 && len(file.Hashes.Pieces.SHA256) == 0 {
		file.Hashes.Pieces = nil
	}
}

// EncodeGeneratedSeed serializes a seed in the requested container format.
func EncodeGeneratedSeed(seed *torrent.Seed, format string, standardPieces []byte) ([]byte, error) {
	if format != "torrent" {
		return torrent.EncodeSeed(seed, format)
	}
	t := &torrent.Torrent{
		Info:         torrent.TorrentInfo{Name: seed.Name, PieceLength: seed.PieceSize, Pieces: standardPieces},
		Comment:      seed.Comment,
		CreatedBy:    seed.CreatedBy,
		CreationDate: time.Now().Unix(),
		OpenList:     seed,
	}
	if len(seed.Trackers) > 0 {
		t.Announce = seed.Trackers[0]
		for _, tracker := range seed.Trackers {
			t.AnnounceList = append(t.AnnounceList, []string{tracker})
		}
	}
	if len(seed.Files) == 1 {
		file := seed.Files[0]
		t.Info.Name = stdpath.Base(file.Path)
		t.Info.Length = file.Size
		t.Info.MD5Sum = file.Hashes.MD5
		if file.CASSliceMD5 != "" {
			t.SetCASInfo(&torrent.CASInfo{
				FileMD5: strings.ToUpper(file.Hashes.MD5), SliceMD5: strings.ToUpper(file.CASSliceMD5),
				SliceSize: torrent.DefaultPieceSize, Cloud: "189",
			})
		} else if seed.PieceSize == torrent.DefaultPieceSize && file.Hashes.Pieces != nil && len(file.Hashes.Pieces.MD5) > 0 {
			t.SetCASInfo(torrent.BuildCASInfoFromMD5s(file.Hashes.MD5, file.Hashes.Pieces.MD5, torrent.DefaultPieceSize))
		}
	} else {
		for _, file := range seed.Files {
			t.Info.Files = append(t.Info.Files, torrent.TorrentFile{Length: file.Size, Path: strings.Split(file.Path, "/"), MD5Sum: file.Hashes.MD5})
		}
	}
	return t.Encode()
}

// GenerateSeedArtifacts reads each file once while computing the requested
// hashes, then emits one or more seed containers. It has no HTTP dependency and
// can be driven synchronously or from a background task.
func GenerateSeedArtifacts(ctx context.Context, user *model.User, params SeedGenerateParams) ([]SeedArtifact, *torrent.Seed, error) {
	if len(params.Paths) == 0 || len(params.Paths) > torrent.DefaultMaxSeedFiles {
		return nil, nil, fmt.Errorf("invalid seed file count")
	}
	formats, err := seedFormats(params)
	if err != nil {
		return nil, nil, err
	}
	matrix := normalizedSeedMatrix(params.HashMatrix, formats)
	pieceSize := params.PieceSize
	if pieceSize <= 0 {
		pieceSize = torrent.DefaultPieceSize
	}
	if slices.Contains(formats, "cas") {
		pieceSize = torrent.DefaultPieceSize
	}
	seedName := strings.TrimSpace(params.Name)
	if seedName == "" {
		seedName = DeriveSeedName(params.Paths)
	}
	seed := torrent.NewSeed(seedName, "OpenList", pieceSize)
	seed.Comment = params.Comment
	seed.Trackers = params.Trackers
	seed.Channels = params.Channels
	shareSet := make(map[string]bool, len(params.ShareFiles))
	for _, p := range params.ShareFiles {
		if strings.TrimSpace(p) != "" {
			shareSet[p] = true
		}
	}
	directSet := make(map[string]bool, len(params.DirectFiles))
	for _, p := range params.DirectFiles {
		if strings.TrimSpace(p) != "" {
			directSet[p] = true
		}
	}
	useGlobalShare := len(shareSet) == 0 && params.IncludeShare
	useGlobalDirect := len(directSet) == 0 && params.IncludeDirectSource
	hasShare := useGlobalShare || len(shareSet) > 0
	hasDirect := useGlobalDirect || len(directSet) > 0
	if hasShare && !user.CanShare() {
		return nil, nil, errs.PermissionDenied
	}
	if hasDirect && setting.GetBool(conf.SignAll) && !hasShare {
		return nil, nil, fmt.Errorf("direct sources require an automatic share when global signing is enabled")
	}
	if (hasShare || hasDirect) && strings.TrimSpace(setting.GetStr(conf.SeedSiteURL)) == "" {
		return nil, nil, fmt.Errorf("seed_site_url must be configured before embedding download sources")
	}
	globalHasher := torrent.NewHashWriter(pieceSize, pieceSize)
	fullPaths := make([]string, 0, len(params.Paths))
	var total int64
	for _, requestedPath := range params.Paths {
		fullPath, err := user.JoinPath(requestedPath)
		if err != nil {
			return nil, nil, err
		}
		meta, err := op.GetNearestMeta(fullPath)
		if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			return nil, nil, err
		}
		if !common.CanRead(user, meta, fullPath) {
			return nil, nil, errs.PermissionDenied
		}
		storage, actualPath, err := op.GetStorageAndActualPath(fullPath)
		if err != nil {
			return nil, nil, err
		}
		obj, err := op.Get(ctx, storage, actualPath)
		if err != nil || obj.IsDir() {
			return nil, nil, fmt.Errorf("seed path must be a readable file: %s", requestedPath)
		}
		total += obj.GetSize()
		modified := ""
		if !obj.ModTime().IsZero() {
			modified = obj.ModTime().UTC().Format(time.RFC3339)
		}
		seedPath := stdpath.Base(requestedPath)
		if len(params.Paths) > 1 {
			seedPath = strings.TrimPrefix(stdpath.Clean(requestedPath), "/")
		}

		hashInfo := obj.GetHash()
		if canReuseListedHashes(hashInfo, matrix) {
			seedFile := torrent.SeedFile{
				Path:     seedPath,
				Size:     obj.GetSize(),
				Modified: modified,
				Hashes: torrent.SeedHashes{
					MD5:    strings.ToLower(hashInfo.GetHash(utils.MD5)),
					SHA1:   strings.ToLower(hashInfo.GetHash(utils.SHA1)),
					SHA256: strings.ToLower(hashInfo.GetHash(utils.SHA256)),
				},
			}
			applySeedMatrix(&seedFile, matrix)
			if comment := strings.TrimSpace(params.FileComments[requestedPath]); comment != "" {
				seedFile.Comment = comment
			} else if comment := strings.TrimSpace(params.FileComments[obj.GetName()]); comment != "" {
				seedFile.Comment = comment
			}
			if useGlobalDirect || directSet[requestedPath] {
				baseURL := strings.TrimRight(setting.GetStr(conf.SeedSiteURL), "/")
				seedFile.Sources = []torrent.SeedSource{{Type: "openlist-direct", URL: baseURL + utils.EncodePath("/d"+fullPath)}}
			}
			seed.Files = append(seed.Files, seedFile)
			fullPaths = append(fullPaths, fullPath)
			continue
		}

		link, _, err := op.Link(ctx, storage, actualPath, model.LinkArgs{})
		if err != nil {
			return nil, nil, fmt.Errorf("storage cannot stream %s: %v", requestedPath, err)
		}
		rangeReader, err := stream.GetRangeReaderFromLink(obj.GetSize(), link)
		if err != nil {
			return nil, nil, fmt.Errorf("storage cannot stream %s", requestedPath)
		}
		rc, err := rangeReader.RangeRead(ctx, http_range.Range{Length: obj.GetSize()})
		if err != nil {
			return nil, nil, err
		}
		fileHasher := torrent.NewHashWriter(pieceSize, pieceSize)
		n, copyErr := io.Copy(io.MultiWriter(globalHasher, fileHasher), rc)
		_ = rc.Close()
		if copyErr != nil {
			return nil, nil, fmt.Errorf("read %s: %w", requestedPath, copyErr)
		}
		if n != obj.GetSize() {
			return nil, nil, fmt.Errorf("read %s: got %d of %d bytes", requestedPath, n, obj.GetSize())
		}
		fileHasher.Finish()
		seedFile := fileHasher.BuildSeedFile(seedPath, modified)
		applySeedMatrix(&seedFile, matrix)
		if comment := strings.TrimSpace(params.FileComments[requestedPath]); comment != "" {
			seedFile.Comment = comment
		} else if comment := strings.TrimSpace(params.FileComments[obj.GetName()]); comment != "" {
			seedFile.Comment = comment
		}
		if useGlobalDirect || directSet[requestedPath] {
			baseURL := strings.TrimRight(setting.GetStr(conf.SeedSiteURL), "/")
			seedFile.Sources = []torrent.SeedSource{{Type: "openlist-direct", URL: baseURL + utils.EncodePath("/d"+fullPath)}}
		}
		seed.Files = append(seed.Files, seedFile)
		fullPaths = append(fullPaths, fullPath)
	}
	globalHasher.Finish()
	createdShares := make([]string, 0, len(seed.Files))
	keepCreatedShares := false
	defer func() {
		if !keepCreatedShares {
			for _, createdID := range createdShares {
				_ = op.DeleteSharing(createdID)
			}
		}
	}()
	if hasShare {
		for index, fullPath := range fullPaths {
			if !useGlobalShare && !shareSet[params.Paths[index]] {
				continue
			}
			sharing := &model.Sharing{
				SharingDB: &model.SharingDB{Remark: "Transfer seed source"},
				Files:     []string{fullPath}, Creator: user,
			}
			shareID, createErr := op.CreateSharing(sharing)
			if createErr != nil {
				return nil, nil, fmt.Errorf("create seed share: %w", createErr)
			}
			createdShares = append(createdShares, shareID)
			seed.Files[index].Sources = []torrent.SeedSource{{
				Type: "openlist-share", URL: strings.TrimRight(setting.GetStr(conf.SeedSiteURL), "/") + "/sd/" + shareID,
				ShareID: shareID,
			}}
		}
	}
	if err := torrent.ValidateSeed(seed, torrent.DefaultParseLimits()); err != nil {
		return nil, nil, err
	}
	outputPath := strings.TrimSpace(params.OutputPath)
	artifacts := make([]SeedArtifact, 0, len(formats))
	seenFormats := make(map[string]struct{}, len(formats))

	// writeArtifact persists the encoded container into the destination folder
	// when an output path is configured, returning the fully-populated artifact.
	writeArtifact := func(format, fileName string, data []byte) (SeedArtifact, error) {
		artifact := SeedArtifact{
			Format:   format,
			Name:     fileName,
			FileName: fileName,
			SeedData: base64.StdEncoding.EncodeToString(data),
			Size:     len(data),
		}
		if outputPath == "" {
			return artifact, nil
		}
		dstDir, err := user.JoinPath(outputPath)
		if err != nil {
			return artifact, err
		}
		meta, metaErr := op.GetNearestMeta(dstDir)
		if metaErr != nil && !errors.Is(errors.Cause(metaErr), errs.MetaNotFound) {
			return artifact, metaErr
		}
		if (!user.CanWriteContent() && !common.CanWriteContentBypassUserPerms(meta, dstDir)) || !common.CanWrite(user, meta, dstDir) {
			return artifact, errs.PermissionDenied
		}
		fileStream := &stream.FileStream{
			Ctx:    ctx,
			Obj:    &model.Object{Name: fileName, Size: int64(len(data)), Modified: time.Now()},
			Reader: bytes.NewReader(data), Mimetype: "application/octet-stream",
		}
		if err = PutDirectly(ctx, dstDir, fileStream); err != nil {
			return artifact, err
		}
		artifact.Path = stdpath.Join(outputPath, fileName)
		return artifact, nil
	}

	for _, requestedFormat := range formats {
		format := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(requestedFormat), "."))
		if format == "bt" {
			format = "torrent"
		}
		if _, exists := seenFormats[format]; exists {
			continue
		}
		seenFormats[format] = struct{}{}

		data, err := EncodeGeneratedSeed(seed, format, globalHasher.GetPieceHashes())
		if err != nil {
			return nil, nil, fmt.Errorf("generate %s seed: %w", format, err)
		}
		fileName := stdpath.Base(seed.Name) + "." + format
		artifact, err := writeArtifact(format, fileName, data)
		if err != nil {
			return nil, nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	keepCreatedShares = true
	return artifacts, seed, nil
}

// SeedGenerateTask generates seed containers asynchronously, writing them into
// params.OutputPath so they appear in the target folder once done.
type SeedGenerateTask struct {
	task.TaskExtension
	params SeedGenerateParams
}

func (t *SeedGenerateTask) GetName() string {
	if len(t.params.Paths) == 1 {
		return fmt.Sprintf("generate seed for %s", stdpath.Base(t.params.Paths[0]))
	}
	return fmt.Sprintf("generate seed for %d files", len(t.params.Paths))
}

func (t *SeedGenerateTask) GetStatus() string {
	return "generating seed"
}

func (t *SeedGenerateTask) Run() error {
	t.ClearEndTime()
	t.SetStartTime(time.Now())
	defer func() { t.SetEndTime(time.Now()) }()
	_, _, err := GenerateSeedArtifacts(t.Ctx(), t.Creator, t.params)
	return err
}

var SeedGenerateTaskManager *tache.Manager[*SeedGenerateTask]

// AddSeedGenerateTask schedules an asynchronous seed generation task.
func AddSeedGenerateTask(ctx context.Context, user *model.User, params SeedGenerateParams) (task.TaskExtensionInfo, error) {
	t := &SeedGenerateTask{
		TaskExtension: task.TaskExtension{
			Creator: user,
			ApiUrl:  common.GetApiUrl(ctx),
		},
		params: params,
	}
	SeedGenerateTaskManager.Add(t)
	return t, nil
}
