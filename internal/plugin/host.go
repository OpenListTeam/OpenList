package plugin

import (
	"context"
	"io"
	"maps"

	log "github.com/sirupsen/logrus"
	"github.com/tetratelabs/wazero"

	"github.com/OpenListTeam/OpenList/v4/internal/op"
	plugin_warp "github.com/OpenListTeam/OpenList/v4/internal/plugin/warp"
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
	exportsType.Export("[method]readable.range", host.StreamRange)
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
		log.Errorln(message)
	} else if level.Warn != nil {
		log.Warnln(message)
	} else {
		log.Traceln(message)
	}
}

// load-config: func() -> result<string, string>;
func (host *DriverHost) LoadConfig(driverHandle uint32) witgo.Result[[]byte, string] {
	driver, ok := host.driver.Get(driverHandle)
	if !ok || driver == nil {
		return witgo.Err[[]byte, string]("host.driver is null, loading timing too early")
	}
	return witgo.Ok[[]byte, string](driver.additional.Bytes())
}

// save-config: func(config: string) -> result<_, string>;
func (host *DriverHost) SaveConfig(driverHandle uint32, config []byte) witgo.Result[witgo.Unit, string] {
	driver, ok := host.driver.Get(driverHandle)
	if !ok || driver == nil {
		return witgo.Err[witgo.Unit, string]("host.driver is null, loading timing too early")
	}

	driver.additional.SetBytes(config)
	op.MustSaveDriverStorage(driver)
	return witgo.Ok[witgo.Unit, string](witgo.Unit{})
}

// stream: func() -> result<output-stream, string>;
func (host *DriverHost) Stream(this plugin_warp.UploadReadable) witgo.Result[io_v0_2.OutputStream, string] {
	upload, ok := host.uploads.Get(this)
	if !ok {
		return witgo.Err[io_v0_2.OutputStream, string]("UploadReadable::Stream: ErrorCodeBadDescriptor")
	}
	upload.Mutex.Lock()
	defer upload.Mutex.Unlock()

	if !upload.StreamConsume {
		upload.StreamConsume = true
		streamHandle := host.StreamManager().Add(manager_io.NewAsyncStreamForReader(upload))
		return witgo.Ok[io_v0_2.OutputStream, string](streamHandle)
	}
	return witgo.Err[io_v0_2.OutputStream, string]("UploadReadable::Stream: StreamConsume")
}

// stream-peek: func(offset: u64, len: u64) -> result<output-stream, string>;
func (host *DriverHost) StreamPeek(this plugin_warp.UploadReadable, offset uint64, len uint64) witgo.Result[io_v0_2.OutputStream, string] {
	upload, ok := host.uploads.Get(this)
	if !ok {
		return witgo.Err[io_v0_2.OutputStream]("UploadReadable::StreamPeek: ErrorCodeBadDescriptor")
	}

	if upload.StreamConsume {
		return witgo.Err[io_v0_2.OutputStream]("UploadReadable::StreamPeek: StreamConsume")
	}

	upload.Mutex.Lock()
	defer upload.Mutex.Unlock()

	if upload.PeekUseing {
		return witgo.Err[io_v0_2.OutputStream]("UploadReadable::StreamPeek: PeekUseing")
	}

	if upload.RangeUseing {
		return witgo.Err[io_v0_2.OutputStream]("UploadReadable::StreamPeek: RangeUseing")
	}

	upload.PeekUseing = true
	peekReader, err := upload.RangeRead(http_range.Range{Start: int64(offset), Length: int64(len)})
	if err != nil {
		return witgo.Err[io_v0_2.OutputStream](err.Error())
	}

	peekReadCloser := utils.NewReadCloser(peekReader, func() error {
		upload.PeekUseing = false
		return nil
	})

	streamHandle := host.StreamManager().Add(&manager_io.Stream{Reader: peekReadCloser, Closer: peekReadCloser})
	return witgo.Ok[io_v0_2.OutputStream, string](streamHandle)
}

// stream-range: func(offset: u64, len: u64) -> result<output-stream, string>;
func (host *DriverHost) StreamRange(this plugin_warp.UploadReadable, offset uint64, len uint64) witgo.Result[io_v0_2.OutputStream, string] {
	upload, ok := host.uploads.Get(this)
	if !ok {
		return witgo.Err[io_v0_2.OutputStream]("UploadReadable::StreamRange: ErrorCodeBadDescriptor")
	}

	upload.Mutex.Lock()
	defer upload.Mutex.Unlock()

	if upload.StreamConsume {
		return witgo.Err[io_v0_2.OutputStream]("UploadReadable::StreamRange: StreamConsume")
	}

	if upload.PeekUseing {
		return witgo.Err[io_v0_2.OutputStream]("UploadReadable::StreamPeek: PeekUseing")
	}

	upload.RangeUseing = true

	var err error
	file := upload.GetFile()
	if file == nil {
		file, err = upload.CacheFullAndWriter(nil, nil)
	}
	if err != nil {
		return witgo.Err[io_v0_2.OutputStream](err.Error())
	}

	peekReader := io.NewSectionReader(file, int64(offset), int64(len))
	if err != nil {
		return witgo.Err[io_v0_2.OutputStream](err.Error())
	}

	streamHandle := host.StreamManager().Add(&manager_io.Stream{Reader: peekReader, Seeker: peekReader})
	return witgo.Ok[io_v0_2.OutputStream, string](streamHandle)
}

// get-hasher: func(hashs: list<hash-alg>) -> result<hash-info, string>;
func (host *DriverHost) GetHasher(this plugin_warp.UploadReadable, hashs []plugin_warp.HashAlg) witgo.Result[[]plugin_warp.HashInfo, string] {
	upload, ok := host.uploads.Get(this)
	if !ok {
		return witgo.Err[[]plugin_warp.HashInfo]("UploadReadable: ErrorCodeBadDescriptor")
	}
	upload.Mutex.Lock()
	defer upload.Mutex.Unlock()

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
	if _, err := upload.CacheFullAndWriter(nil, hashers); err != nil {
		return witgo.Err[[]plugin_warp.HashInfo](err.Error())
	}

	maps.Copy(upload.GetHash().Export(), hashers.GetHashInfo().Export())

	return witgo.Ok[[]plugin_warp.HashInfo, string](plugin_warp.HashInfoConvert(*hashers.GetHashInfo()))
}

// update-progress: func(progress: f64);
func (host *DriverHost) UpdateProgress(this plugin_warp.UploadReadable, progress float64) {
	upload, ok := host.uploads.Get(this)
	if ok && upload.UpdateProgress != nil {
		upload.Mutex.Lock()
		defer upload.Mutex.Unlock()
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
