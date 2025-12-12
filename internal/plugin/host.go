package plugin

import (
	"context"
	"io"
	"maps"

	log "github.com/sirupsen/logrus"
	"github.com/tetratelabs/wazero"

	"github.com/OpenListTeam/OpenList/v4/internal/op"
	plugin_warp "github.com/OpenListTeam/OpenList/v4/internal/plugin/warp"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"

	manager_io "github.com/OpenListTeam/wazero-wasip2/manager/io"
	"github.com/OpenListTeam/wazero-wasip2/wasip2"
	wasi_clocks "github.com/OpenListTeam/wazero-wasip2/wasip2/clocks"
	wasi_filesystem "github.com/OpenListTeam/wazero-wasip2/wasip2/filesystem"
	wasi_http "github.com/OpenListTeam/wazero-wasip2/wasip2/http"
	wasi_io "github.com/OpenListTeam/wazero-wasip2/wasip2/io"
	io_v0_2 "github.com/OpenListTeam/wazero-wasip2/wasip2/io/v0_2"
	wasi_random "github.com/OpenListTeam/wazero-wasip2/wasip2/random"
	wasi_sockets "github.com/OpenListTeam/wazero-wasip2/wasip2/sockets"
	witgo "github.com/OpenListTeam/wazero-wasip2/wit-go"
)

type DriverHost struct {
	*wasip2.Host
	contexts *plugin_warp.ContextManaget
	uploads  *plugin_warp.UploadReadableManager

	driver *witgo.ResourceManager[*WasmDriver]
}

func NewDriverHost() *DriverHost {
	waspi2_host := wasip2.NewHost(
		wasi_io.Module("0.2.2"),
		wasi_filesystem.Module("0.2.2"),
		wasi_random.Module("0.2.2"),
		wasi_clocks.Module("0.2.2"),
		wasi_sockets.Module("0.2.0"),
		wasi_http.Module("0.2.0"),
	)
	return &DriverHost{
		Host:     waspi2_host,
		contexts: plugin_warp.NewContextManager(),
		uploads:  plugin_warp.NewUploadManager(),
		driver:   witgo.NewResourceManager[*WasmDriver](nil),
	}
}

func (host *DriverHost) Instantiate(ctx context.Context, rt wazero.Runtime) error {
	if err := host.Host.Instantiate(ctx, rt); err != nil {
		return err
	}

	module := rt.NewHostModuleBuilder("openlist:plugin-driver/host@0.1.0")
	exports := witgo.NewExporter(module)

	exports.Export("log", host.Log)
	exports.Export("load-config", host.LoadConfig)
	exports.Export("save-config", host.SaveConfig)
	if _, err := exports.Instantiate(ctx); err != nil {
		return err
	}

	moduleType := rt.NewHostModuleBuilder("openlist:plugin-driver/types@0.1.0")
	exportsType := witgo.NewExporter(moduleType)
	exportsType.Export("[resource-drop]cancellable", host.DropContext)
	exportsType.Export("[method]cancellable.subscribe", host.Subscribe)

	exportsType.Export("[resource-drop]readable", host.DropReadable)
	exportsType.Export("[method]readable.streams", host.Stream)
	exportsType.Export("[method]readable.peek", host.StreamPeek)
	exportsType.Export("[method]readable.chunks", host.Chunks)
	exportsType.Export("[method]readable.next-chunk", host.NextChunk)
	exportsType.Export("[method]readable.chunk-reset", host.ChunkReset)
	exportsType.Export("[method]readable.get-hasher", host.GetHasher)
	exportsType.Export("[method]readable.update-progress", host.UpdateProgress)
	if _, err := exportsType.Instantiate(ctx); err != nil {
		return err
	}

	return nil
}

func (host *DriverHost) ContextManager() *plugin_warp.ContextManaget {
	return host.contexts
}

func (host *DriverHost) UploadManager() *plugin_warp.UploadReadableManager {
	return host.uploads
}

func (host *DriverHost) DropReadable(this plugin_warp.UploadReadable) {
	host.uploads.Remove(this)
}

func (host *DriverHost) DropContext(this plugin_warp.Context) {
	host.contexts.Remove(this)
}

// log: func(level: log-level, message: string);
func (host *DriverHost) Log(level plugin_warp.LogLevel, message string) {
	if level.Debug != nil {
		log.Debugln(message)
	} else if level.Error != nil {
		log.Errorln(message)
	} else if level.Info != nil {
		log.Infoln(message)
	} else if level.Warn != nil {
		log.Warnln(message)
	} else {
		log.Traceln(message)
	}
}

// load-config: func(driver: u32) -> result<list<u8>, string>;
func (host *DriverHost) LoadConfig(driverHandle uint32) witgo.Result[[]byte, string] {
	driver, ok := host.driver.Get(driverHandle)
	if !ok || driver == nil {
		return witgo.Err[[]byte]("host.driver is null, loading timing too early")
	}
	return witgo.Ok[[]byte, string](driver.additional.Bytes())
}

// save-config: func(driver: u32, config: list<u8>) -> result<_, string>;
func (host *DriverHost) SaveConfig(driverHandle uint32, config []byte) witgo.Result[witgo.Unit, string] {
	driver, ok := host.driver.Get(driverHandle)
	if !ok || driver == nil {
		return witgo.Err[witgo.Unit]("host.driver is null, loading timing too early")
	}

	driver.additional.SetBytes(config)
	op.MustSaveDriverStorage(driver)
	return witgo.Ok[witgo.Unit, string](witgo.Unit{})
}

// streams: func() -> result<input-stream, string>;
func (host *DriverHost) Stream(this plugin_warp.UploadReadable) witgo.Result[io_v0_2.InputStream, string] {
	upload, ok := host.uploads.Get(this)
	if !ok {
		return witgo.Err[io_v0_2.InputStream]("UploadReadable::Stream: ErrorCodeBadDescriptor")
	}
	if upload.StreamConsume {
		return witgo.Err[io_v0_2.InputStream]("UploadReadable::Stream: StreamConsume")
	}

	upload.StreamConsume = true
	streamHandle := host.StreamManager().Add(&manager_io.Stream{Reader: upload, Seeker: upload.GetFile()})
	return witgo.Ok[io_v0_2.InputStream, string](streamHandle)
}

// peek: func(offset: u64, len: u64) -> result<input-stream, string>;
func (host *DriverHost) StreamPeek(this plugin_warp.UploadReadable, offset uint64, len uint64) witgo.Result[io_v0_2.InputStream, string] {
	upload, ok := host.uploads.Get(this)
	if !ok {
		return witgo.Err[io_v0_2.InputStream]("UploadReadable::StreamPeek: ErrorCodeBadDescriptor")
	}
	if upload.StreamConsume {
		return witgo.Err[io_v0_2.InputStream]("UploadReadable::StreamPeek: StreamConsume")
	}

	peekReader, err := upload.RangeRead(http_range.Range{Start: int64(offset), Length: int64(len)})
	if err != nil {
		return witgo.Err[io_v0_2.InputStream](err.Error())
	}
	seeker, _ := peekReader.(io.Seeker)
	streamHandle := host.StreamManager().Add(&manager_io.Stream{Reader: peekReader, Seeker: seeker})
	return witgo.Ok[io_v0_2.InputStream, string](streamHandle)
}

// chunks: func(len: u32) -> result<u32, string>;
func (host *DriverHost) Chunks(this plugin_warp.UploadReadable, len uint32) witgo.Result[uint32, string] {
	upload, ok := host.uploads.Get(this)
	if !ok {
		return witgo.Err[uint32]("UploadReadable::Chunks: ErrorCodeBadDescriptor")
	}
	if upload.StreamConsume {
		return witgo.Err[uint32]("UploadReadable::Chunks: StreamConsume")
	}
	if upload.SectionReader != nil {
		return witgo.Err[uint32]("UploadReadable::Chunks: Already exist chunk reader")
	}

	ss, err := stream.NewStreamSectionReader(upload, int(len), &upload.UpdateProgress)
	if err != nil {
		return witgo.Err[uint32](err.Error())
	}
	chunkSize := int64(len)
	upload.SectionReader = &plugin_warp.StreamSectionReader{StreamSectionReaderIF: ss, CunketSize: chunkSize}
	return witgo.Ok[uint32, string](uint32((upload.GetSize() + chunkSize - 1) / chunkSize))
}

// next-chunk: func() -> result<input-stream, string>;
func (host *DriverHost) NextChunk(this plugin_warp.UploadReadable) witgo.Result[io_v0_2.InputStream, string] {
	upload, ok := host.uploads.Get(this)
	if !ok {
		return witgo.Err[io_v0_2.InputStream]("UploadReadable::NextChunk: ErrorCodeBadDescriptor")
	}
	if upload.SectionReader == nil {
		return witgo.Err[io_v0_2.InputStream]("UploadReadable::NextChunk: No chunk reader")
	}

	chunkSize := min(upload.SectionReader.CunketSize, upload.GetSize()-upload.SectionReader.Offset)
	sr, err := upload.SectionReader.GetSectionReader(upload.SectionReader.Offset, chunkSize)
	if err != nil {
		return witgo.Err[io_v0_2.InputStream](err.Error())
	}
	upload.SectionReader.Offset += chunkSize
	streamHandle := host.StreamManager().Add(&manager_io.Stream{Reader: sr, Seeker: sr, Closer: utils.CloseFunc(func() error {
		upload.SectionReader.FreeSectionReader(sr)
		return nil
	})})
	return witgo.Ok[io_v0_2.InputStream, string](streamHandle)
}

// chunk-reset: func(chunk: input-stream) -> result<_, string>;
func (host *DriverHost) ChunkReset(this plugin_warp.UploadReadable, chunk io_v0_2.InputStream) witgo.Result[witgo.Unit, string] {
	stream, ok := host.StreamManager().Get(chunk)
	if !ok {
		return witgo.Err[witgo.Unit]("UploadReadable::ChunkReset: ErrorCodeBadDescriptor")
	}
	if stream.Seeker == nil {
		return witgo.Err[witgo.Unit]("UploadReadable::ChunkReset: Not Seeker")
	}
	_, err := stream.Seeker.Seek(0, io.SeekStart)
	if err != nil {
		return witgo.Err[witgo.Unit](err.Error())
	}
	return witgo.Ok[witgo.Unit, string](witgo.Unit{})
}

// get-hasher: func(hashs: list<hash-alg>) -> result<list<hash-info>, string>;
func (host *DriverHost) GetHasher(this plugin_warp.UploadReadable, hashs []plugin_warp.HashAlg) witgo.Result[[]plugin_warp.HashInfo, string] {
	upload, ok := host.uploads.Get(this)
	if !ok {
		return witgo.Err[[]plugin_warp.HashInfo]("UploadReadable: ErrorCodeBadDescriptor")
	}

	resultHashs := plugin_warp.HashInfoConvert2(upload.GetHash(), hashs)
	if resultHashs != nil {
		return witgo.Ok[[]plugin_warp.HashInfo, string](resultHashs)
	}

	if upload.StreamConsume {
		return witgo.Err[[]plugin_warp.HashInfo]("UploadReadable: StreamConsume")
	}

	// 无法从obj中获取需要的hash，或者获取的hash不完整。
	// 需要缓存整个文件并进行hash计算
	hashTypes := plugin_warp.HashAlgConverts(hashs)

	hashers := utils.NewMultiHasher(hashTypes)
	if _, err := upload.CacheFullAndWriter(&upload.UpdateProgress, hashers); err != nil {
		return witgo.Err[[]plugin_warp.HashInfo](err.Error())
	}

	maps.Copy(upload.GetHash().Export(), hashers.GetHashInfo().Export())

	return witgo.Ok[[]plugin_warp.HashInfo, string](plugin_warp.HashInfoConvert(*hashers.GetHashInfo()))
}

// update-progress: func(progress: f64);
func (host *DriverHost) UpdateProgress(this plugin_warp.UploadReadable, progress float64) {
	upload, ok := host.uploads.Get(this)
	if ok {
		upload.UpdateProgress(progress)
	}
}

// resource cancellable { subscribe: func() -> pollable; }
func (host *DriverHost) Subscribe(this plugin_warp.Context) io_v0_2.Pollable {
	poll := host.Host.PollManager()

	ctx, ok := host.contexts.Get(this)
	if !ok {
		return poll.Add(manager_io.ReadyPollable)
	}

	return poll.Add(&plugin_warp.ContextPollable{Context: ctx})
}
