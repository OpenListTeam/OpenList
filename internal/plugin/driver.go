package plugin

import (
	"context"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"sync/atomic"

	"github.com/OpenListTeam/OpenList/v4/internal/alloc"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	plugin_warp "github.com/OpenListTeam/OpenList/v4/internal/plugin/warp"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"

	manager_io "github.com/OpenListTeam/wazero-wasip2/manager/io"
	io_v_0_2 "github.com/OpenListTeam/wazero-wasip2/wasip2/io/v0_2"
	witgo "github.com/OpenListTeam/wazero-wasip2/wit-go"

	"github.com/pkg/errors"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

var PluginPrefix = "openlist:plugin-driver/exports@0.1.0#"

type DriverPlugin struct {
	runtime  wazero.Runtime
	instance api.Module
	exports  *DriverHost
	guest    *witgo.Host
}

func NewDriverPlugin(ctx context.Context, plugin *PluginInfo) (*DriverPlugin, error) {
	wasmBytes, err := os.ReadFile(plugin.WasmPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read wasm file '%s': %w", plugin.WasmPath, err)
	}

	// 初始化 Wazero 运行时，并导入 WASI
	rt := wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)

	driverHost := NewDriverHost()
	if err := driverHost.Instantiate(ctx, rt); err != nil {
		rt.Close(ctx)
		log.Fatalf("编译模块失败: %v", err)
	}

	compiledModule, err := rt.CompileModule(ctx, wasmBytes)
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("failed to compile wasm module for plugin '%s': %w", plugin.ID, err)
	}

	// 为这个驱动实例创建一个独立的模块配置，特别是文件系统
	moduleConfig := wazero.NewModuleConfig().
		WithFS(os.DirFS("/")). // 示例：可以根据需要配置虚拟文件系统
		WithStartFunctions("_initialize").
		WithStdout(os.Stdout).
		WithStderr(os.Stderr).
		WithStdin(os.Stdin).
		WithSysNanosleep().
		WithSysNanotime().
		WithSysWalltime().
		WithOsyield(func() {
			runtime.Gosched()
		}).
		WithName(plugin.ID)

	ctx = experimental.WithMemoryAllocator(ctx, experimental.MemoryAllocatorFunc(alloc.NewMemory))
	// 实例化模块，同时注入 Host API
	instance, err := rt.InstantiateModule(ctx, compiledModule, moduleConfig)
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("failed to instantiate module: %w", err)
	}

	guest, err := witgo.NewHost(instance)
	if err != nil {
		rt.Close(ctx)
		return nil, err
	}

	driver := &DriverPlugin{
		runtime:  rt,
		instance: instance,
		exports:  driverHost,
		guest:    guest,
	}
	return driver, nil
}

func (d *DriverPlugin) Close(ctx context.Context) error {
	return d.runtime.Close(ctx)
}

func (d *DriverPlugin) NewWasmDriver() (driver.Driver, error) {
	var driverHandle uint32
	err := d.guest.Call(context.Background(), PluginPrefix+"[constructor]driver", &driverHandle)
	if err != nil {
		return nil, err
	}

	driver := &WasmDriver{
		plugin: d,
		handle: driverHandle,
	}

	if driver.config, err = driver.GetProperties(); err != nil {
		return nil, err
	}

	if driver.additional.Forms, err = driver.GetFormMeta(); err != nil {
		return nil, err
	}

	type WasmDirverWarp struct {
		*WasmDriver
	}
	driverWarp := &WasmDirverWarp{driver}
	driver.plugin.exports.driver.Set(driver.handle, driver)
	runtime.SetFinalizer(driverWarp, func(driver *WasmDirverWarp) {
		log.Infof("runtime.SetFinalizer: %s => %d", driver.config.Name, driver.handle)
		driver.plugin.guest.Call(context.Background(), PluginPrefix+"[dtor]driver", driver.handle)
		driver.plugin.exports.driver.Remove(driver.handle)
	})
	return driverWarp, nil
}

// WasmDriver 实现了 Driver 接口，并代理调用到 Wasm 模块
type WasmDriver struct {
	model.Storage
	flag uint32

	plugin *DriverPlugin
	handle uint32

	config     plugin_warp.DriverProps
	additional plugin_warp.Additional
}

// 处理wasm驱动返回的错误，当错误类型为Unauthorized将驱动状态设置为nowork避免无效时继续访问
func (d *WasmDriver) handleError(errcode *plugin_warp.ErrCode) error {
	if errcode != nil {
		err := errcode.ToError()
		if errcode.Unauthorized != nil && d.Status == op.WORK {
			if atomic.CompareAndSwapUint32(&d.flag, 0, 1) {
				d.Status = err.Error()
				op.MustSaveDriverStorage(d)
				atomic.StoreUint32(&d.flag, 0)
			}
			return err
		}
		return err
	}
	return nil
}

func (d *WasmDriver) GetProperties() (plugin_warp.DriverProps, error) {
	var propertiesResult plugin_warp.DriverProps
	err := d.plugin.guest.Call(context.Background(), PluginPrefix+"[method]driver.get-properties", &propertiesResult, d.handle)
	return propertiesResult, err
}

func (d *WasmDriver) GetFormMeta() ([]plugin_warp.FormField, error) {
	var formMeta []plugin_warp.FormField
	err := d.plugin.guest.Call(context.Background(), PluginPrefix+"[method]driver.get-form-meta", &formMeta, d.handle)
	return formMeta, err
}

func (d *WasmDriver) Config() driver.Config {
	newconfig, err := d.GetProperties()
	if err != nil {
		panic(err)
	}
	d.config = newconfig
	return d.config.ToConfig()
}

func (d *WasmDriver) GetAddition() driver.Additional {
	newFormMeta, err := d.GetFormMeta()
	if err != nil {
		panic(err)
	}
	d.additional.Forms = newFormMeta
	return &d.additional
}

// Init 初始化驱动
func (d *WasmDriver) Init(ctx context.Context) error {
	ctxHandle := d.plugin.exports.ContextManager().Add(ctx)
	defer d.plugin.exports.ContextManager().Remove(ctxHandle)

	var result witgo.Result[witgo.Unit, plugin_warp.ErrCode]
	if err := d.plugin.guest.Call(ctx, PluginPrefix+"[method]driver.init", &result, d.handle, ctxHandle); err != nil {
		// 这里就不返回错误了,避免大量栈数据
		log.Errorln(err)
		return errors.New("Internal error in plugin")
	}
	if result.Err != nil {
		return result.Err.ToError()
	}

	return nil
}

// Drop 销毁驱动
func (d *WasmDriver) Drop(ctx context.Context) error {
	ctxHandle := d.plugin.exports.ContextManager().Add(ctx)
	defer d.plugin.exports.ContextManager().Remove(ctxHandle)

	var result witgo.Result[witgo.Unit, plugin_warp.ErrCode]
	if err := d.plugin.guest.Call(ctx, PluginPrefix+"[method]driver.drop", &result, d.handle, ctxHandle); err != nil {
		// 这里就不返回错误了,避免大量栈数据
		log.Errorln(err)
		return errors.New("Internal error in plugin")
	}
	if result.Err != nil {
		return result.Err.ToError()
	}
	return nil
}

func (d *WasmDriver) GetRoot(ctx context.Context) (model.Obj, error) {
	if !d.config.Capabilitys.ListFile {
		return nil, errs.NotImplement
	}
	ctxHandle := d.plugin.exports.ContextManager().Add(ctx)
	defer d.plugin.exports.ContextManager().Remove(ctxHandle)

	var result witgo.Result[plugin_warp.Object, plugin_warp.ErrCode]
	err := d.plugin.guest.Call(ctx, PluginPrefix+"[method]driver.get-root", &result, d.handle, ctxHandle)
	if err != nil {
		if errors.Is(err, witgo.ErrNotExportFunc) {
			return nil, errs.NotImplement
		}
		// 这里就不返回错误了,避免大量栈数据
		log.Errorln(err)
		return nil, errors.New("Internal error in plugin")
	}

	if result.Err != nil {
		return nil, d.handleError(result.Err)
	}

	return result.Ok, nil
}

// GetFile 获取文件信息
func (d *WasmDriver) Get(ctx context.Context, path string) (model.Obj, error) {
	if !d.config.Capabilitys.GetFile {
		return nil, errs.NotImplement
	}

	ctxHandle := d.plugin.exports.ContextManager().Add(ctx)
	defer d.plugin.exports.ContextManager().Remove(ctxHandle)

	var result witgo.Result[plugin_warp.Object, plugin_warp.ErrCode]
	err := d.plugin.guest.Call(ctx, PluginPrefix+"[method]driver.get-file", &result, d.handle, ctxHandle, path)
	if err != nil {
		if errors.Is(err, witgo.ErrNotExportFunc) {
			return nil, errs.NotImplement
		}
		// 这里就不返回错误了,避免大量栈数据
		log.Errorln(err)
		return nil, errors.New("Internal error in plugin")
	}
	if result.Err != nil {
		return nil, d.handleError(result.Err)
	}

	return result.Ok, nil
}

// List 列出文件
func (d *WasmDriver) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if !d.config.Capabilitys.ListFile {
		return nil, errs.NotImplement
	}

	ctxHandle := d.plugin.exports.ContextManager().Add(ctx)
	defer d.plugin.exports.ContextManager().Remove(ctxHandle)

	obj := dir.(*plugin_warp.Object)

	var result witgo.Result[[]plugin_warp.Object, plugin_warp.ErrCode]

	param := struct {
		Driver uint32
		Handle plugin_warp.Context
		Obj    *plugin_warp.Object
	}{d.handle, ctxHandle, obj}
	err := d.plugin.guest.Call(ctx, PluginPrefix+"[method]driver.list-files", &result, param)
	if err != nil {
		if errors.Is(err, witgo.ErrNotExportFunc) {
			return nil, errs.NotImplement
		}
		// 这里就不返回错误了,避免大量栈数据
		log.Errorln(err)
		return nil, errors.New("Internal error in plugin")
	}

	if result.Err != nil {
		return nil, d.handleError(result.Err)
	}
	return utils.MustSliceConvert(*result.Ok, func(o plugin_warp.Object) model.Obj { return &o }), nil
}

// Link 获取文件直链或读取流
func (d *WasmDriver) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if !d.config.Capabilitys.LinkFile {
		return nil, errs.NotImplement
	}

	// 这部分资源全由Host端管理
	// TODO: 或许应该把创建的Stream生命周期一同绑定到此处结束，防止忘记关闭导致的资源泄漏
	ctxHandle := d.plugin.exports.ContextManager().Add(ctx)
	defer d.plugin.exports.ContextManager().Remove(ctxHandle)
	headersHandle := d.plugin.exports.HTTPManager().Fields.Add(args.Header)
	defer d.plugin.exports.HTTPManager().Fields.Remove(headersHandle)

	obj := file.(*plugin_warp.Object)

	var result witgo.Result[plugin_warp.LinkResult, plugin_warp.ErrCode]

	param := struct {
		Driver   uint32
		Handle   plugin_warp.Context
		Obj      *plugin_warp.Object
		LinkArgs plugin_warp.LinkArgs
	}{d.handle, ctxHandle, obj, plugin_warp.LinkArgs{IP: args.IP, Header: headersHandle}}
	err := d.plugin.guest.Call(ctx, PluginPrefix+"[method]driver.link-file", &result, param)
	if err != nil {
		if errors.Is(err, witgo.ErrNotExportFunc) {
			return nil, errs.NotImplement
		}
		// 这里就不返回错误了,避免大量栈数据
		log.Errorln(err)
		return nil, errors.New("Internal error in plugin")
	}
	if result.Err != nil {
		return nil, d.handleError(result.Err)
	}

	// 覆盖旧的Obj
	if result.Ok.File.IsSome() {
		*obj = *result.Ok.File.Some
	}

	if result.Ok.Resource.Direct != nil {
		direct := result.Ok.Resource.Direct
		header, _ := d.plugin.exports.HTTPManager().Fields.Pop(direct.Header)
		link := &model.Link{URL: direct.Url, Header: http.Header(header)}
		if direct.Expiratcion.IsSome() {
			exp := direct.Expiratcion.Some.ToDuration()
			link.Expiration = &exp
		}
		return link, nil
	}

	if result.Ok.Resource.RangeStream != nil {
		streamManager := d.plugin.exports.StreamManager()
		fileSize := obj.GetSize()
		return &model.Link{
			RangeReader: stream.RateLimitRangeReaderFunc(func(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
				var size uint64
				if httpRange.Length < 0 || httpRange.Start+httpRange.Length > fileSize {
					size = uint64(fileSize - httpRange.Start)
				} else {
					size = uint64(httpRange.Length)
				}
				r, w := io.Pipe()
				cw := &checkWriter{W: w, N: size}
				streamHandle := streamManager.Add(&manager_io.Stream{
					Writer:      cw,
					CheckWriter: cw,
				})
				ctxHandle := d.plugin.exports.ContextManager().Add(ctx)

				type RangeSpec struct {
					Offset uint64
					Size   uint64
					Stream io_v_0_2.OutputStream
				}

				var result witgo.Result[witgo.Unit, plugin_warp.ErrCode]
				param := struct {
					Driver    uint32
					Handle    plugin_warp.Context
					Obj       *plugin_warp.Object
					LinkArgs  plugin_warp.LinkArgs
					RangeSpec RangeSpec
				}{d.handle, ctxHandle, obj, plugin_warp.LinkArgs{IP: args.IP, Header: headersHandle}, RangeSpec{Offset: uint64(httpRange.Start), Size: size, Stream: streamHandle}}

				go func() {
					if err := d.plugin.guest.Call(ctx, PluginPrefix+"[method]driver.link-range", &result, param); err != nil {
						if errors.Is(err, witgo.ErrNotExportFunc) {
							w.CloseWithError(errs.NotImplement)
							return
						}
						// 这里就不返回错误了,避免大量栈数据
						log.Errorln(err)
						w.CloseWithError(err)
						return
					}

					if result.Err != nil {
						w.CloseWithError(d.handleError(result.Err))
						return
					}
				}()

				return utils.NewReadCloser(r, func() error {
					d.plugin.exports.ContextManager().Remove(ctxHandle)
					streamManager.Remove(streamHandle)
					return r.Close()
				}), nil
			}),
		}, nil
	}

	return nil, errs.NotImplement
}

type checkWriter struct {
	W io.Writer
	N uint64
}

func (c *checkWriter) Write(p []byte) (n int, err error) {
	if c.N <= 0 {
		return 0, stderrors.New("write limit exceeded")
	}
	n, err = c.W.Write(p[:min(uint64(len(p)), c.N)])
	c.N -= uint64(n)
	return
}
func (c *checkWriter) CheckWrite() uint64 {
	return max(c.N, 1)
}

func (d *WasmDriver) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	if !d.config.Capabilitys.MkdirFile {
		return nil, errs.NotImplement
	}

	ctxHandle := d.plugin.exports.ContextManager().Add(ctx)
	defer d.plugin.exports.ContextManager().Remove(ctxHandle)

	obj := parentDir.(*plugin_warp.Object)

	var result witgo.Result[witgo.Option[plugin_warp.Object], plugin_warp.ErrCode]

	params := struct {
		Driver  uint32
		Handle  plugin_warp.Context
		Obj     *plugin_warp.Object
		DirName string
	}{d.handle, ctxHandle, obj, dirName}

	if err := d.plugin.guest.Call(ctx, PluginPrefix+"[method]driver.make-dir", &result, params); err != nil {
		if errors.Is(err, witgo.ErrNotExportFunc) {
			return nil, errs.NotImplement
		}
		// 这里就不返回错误了,避免大量栈数据
		log.Errorln(err)
		return nil, errors.New("Internal error in plugin")
	}

	if result.Err != nil {
		return nil, d.handleError(result.Err)
	}

	return result.Ok.Some, nil
}

func (d *WasmDriver) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	if !d.config.Capabilitys.RenameFile {
		return nil, errs.NotImplement
	}

	ctxHandle := d.plugin.exports.ContextManager().Add(ctx)
	defer d.plugin.exports.ContextManager().Remove(ctxHandle)

	obj := srcObj.(*plugin_warp.Object)

	var result witgo.Result[witgo.Option[plugin_warp.Object], plugin_warp.ErrCode]

	params := struct {
		Driver  uint32
		Handle  plugin_warp.Context
		Obj     *plugin_warp.Object
		NewName string
	}{d.handle, ctxHandle, obj, newName}
	err := d.plugin.guest.Call(ctx, PluginPrefix+"[method]driver.rename-file", &result, params)
	if err != nil {
		if errors.Is(err, witgo.ErrNotExportFunc) {
			return nil, errs.NotImplement
		}
		// 这里就不返回错误了,避免大量栈数据
		log.Errorln(err)
		return nil, errors.New("Internal error in plugin")
	}

	if result.Err != nil {
		return nil, d.handleError(result.Err)
	}

	return result.Ok.Some, nil
}

func (d *WasmDriver) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	if !d.config.Capabilitys.MoveFile {
		return nil, errs.NotImplement
	}

	ctxHandle := d.plugin.exports.ContextManager().Add(ctx)
	defer d.plugin.exports.ContextManager().Remove(ctxHandle)

	srcobj := srcObj.(*plugin_warp.Object)
	dstobj := dstDir.(*plugin_warp.Object)

	var result witgo.Result[witgo.Option[plugin_warp.Object], plugin_warp.ErrCode]

	params := struct {
		Driver uint32
		Handle plugin_warp.Context
		SrcObj *plugin_warp.Object
		DstObj *plugin_warp.Object
	}{d.handle, ctxHandle, srcobj, dstobj}
	err := d.plugin.guest.Call(ctx, PluginPrefix+"[method]driver.move-file", &result, params)
	if err != nil {
		if errors.Is(err, witgo.ErrNotExportFunc) {
			return nil, errs.NotImplement
		}
		// 这里就不返回错误了,避免大量栈数据
		log.Errorln(err)
		return nil, errors.New("Internal error in plugin")
	}

	if result.Err != nil {
		return nil, d.handleError(result.Err)
	}

	return result.Ok.Some, nil
}

func (d *WasmDriver) Remove(ctx context.Context, srcObj model.Obj) error {
	if !d.config.Capabilitys.RemoveFile {
		return errs.NotImplement
	}

	ctxHandle := d.plugin.exports.ContextManager().Add(ctx)
	defer d.plugin.exports.ContextManager().Remove(ctxHandle)

	srcobj := srcObj.(*plugin_warp.Object)

	var result witgo.Result[witgo.Unit, plugin_warp.ErrCode]

	params := struct {
		Driver uint32
		Handle plugin_warp.Context
		SrcObj *plugin_warp.Object
	}{d.handle, ctxHandle, srcobj}
	err := d.plugin.guest.Call(ctx, PluginPrefix+"[method]driver.remove-file", &result, params)
	if err != nil {
		if errors.Is(err, witgo.ErrNotExportFunc) {
			return errs.NotImplement
		}
		// 这里就不返回错误了,避免大量栈数据
		log.Errorln(err)
		return errors.New("Internal error in plugin")
	}

	if result.Err != nil {
		return d.handleError(result.Err)
	}

	return nil
}

func (d *WasmDriver) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	if !d.config.Capabilitys.CopyFile {
		return nil, errs.NotImplement
	}

	ctxHandle := d.plugin.exports.ContextManager().Add(ctx)
	defer d.plugin.exports.ContextManager().Remove(ctxHandle)

	srcobj := srcObj.(*plugin_warp.Object)
	dstobj := dstDir.(*plugin_warp.Object)

	var result witgo.Result[witgo.Option[plugin_warp.Object], plugin_warp.ErrCode]

	params := struct {
		Driver uint32
		Handle plugin_warp.Context
		SrcObj *plugin_warp.Object
		DstObj *plugin_warp.Object
	}{d.handle, ctxHandle, srcobj, dstobj}
	err := d.plugin.guest.Call(ctx, PluginPrefix+"[method]driver.copy-file", &result, params)
	if err != nil {
		if errors.Is(err, witgo.ErrNotExportFunc) {
			return nil, errs.NotImplement
		}
		// 这里就不返回错误了,避免大量栈数据
		log.Errorln(err)
		return nil, errors.New("Internal error in plugin")
	}

	if result.Err != nil {
		return nil, d.handleError(result.Err)
	}

	return result.Ok.Some, nil
}

func (d *WasmDriver) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	if !d.config.Capabilitys.UploadFile {
		return nil, errs.NotImplement
	}

	ctxHandle := d.plugin.exports.ContextManager().Add(ctx)
	defer d.plugin.exports.ContextManager().Remove(ctxHandle)

	stream := d.plugin.exports.uploads.Add(&plugin_warp.UploadReadableType{FileStreamer: file, UpdateProgress: up})
	defer d.plugin.exports.uploads.Remove(stream)

	dstobj := dstDir.(*plugin_warp.Object)

	var result witgo.Result[witgo.Option[plugin_warp.Object], plugin_warp.ErrCode]

	exist := witgo.None[plugin_warp.Object]()
	if file.GetExist() != nil {
		exist = witgo.Some(plugin_warp.ConvertObjToObject(file.GetExist()))
	}

	params := struct {
		Driver uint32
		Handle plugin_warp.Context
		DstObj *plugin_warp.Object
		Upload *plugin_warp.UploadRequest
	}{d.handle, ctxHandle, dstobj, &plugin_warp.UploadRequest{
		Target:  plugin_warp.ConvertObjToObject(file),
		Content: stream,
		Exist:   exist,
	}}
	err := d.plugin.guest.Call(ctx, PluginPrefix+"[method]driver.upload-file", &result, params)
	if err != nil {
		if errors.Is(err, witgo.ErrNotExportFunc) {
			return nil, errs.NotImplement
		}
		// 这里就不返回错误了,避免大量栈数据
		log.Errorln(err)
		return nil, errors.New("Internal error in plugin")
	}

	if result.Err != nil {
		return nil, d.handleError(result.Err)
	}

	return result.Ok.Some, nil
}

var _ driver.Meta = (*WasmDriver)(nil)
var _ driver.Reader = (*WasmDriver)(nil)
var _ driver.Getter = (*WasmDriver)(nil)
var _ driver.GetRooter = (*WasmDriver)(nil)
var _ driver.MkdirResult = (*WasmDriver)(nil)
var _ driver.RenameResult = (*WasmDriver)(nil)
var _ driver.MoveResult = (*WasmDriver)(nil)
var _ driver.Remove = (*WasmDriver)(nil)
var _ driver.CopyResult = (*WasmDriver)(nil)
var _ driver.PutResult = (*WasmDriver)(nil)
