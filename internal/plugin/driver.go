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

	pool "github.com/jolestar/go-commons-pool/v2"

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

// DriverPlugin 是*插件*管理器 (每个 .wasm 文件一个)
// 它管理共享的 wazero 资源
type DriverPlugin struct {
	plugin         *PluginInfo
	runtime        wazero.Runtime        // 共享的 wazero 运行时
	compiledModule wazero.CompiledModule // 共享的已编译模块
	host           *DriverHost           // 注册的 wasi host 资源, 这里的self.driver始终为nil
}

// WasmInstance 代表池中的一个可重用对象
// 它包含一个活动的 WASM 实例及其宿主/Guest API
type WasmInstance struct {
	instance api.Module
	exports  *DriverHost
	guest    *witgo.Host
}

// 内部函数，用于动态调用 Guest 以获取属性
func (d *WasmInstance) GetProperties(ctx context.Context) (plugin_warp.DriverProps, error) {
	var propertiesResult plugin_warp.DriverProps
	err := d.guest.Call(ctx, PluginPrefix+"get-properties", &propertiesResult)
	if err != nil {
		return plugin_warp.DriverProps{}, err
	}
	return propertiesResult, nil
}

// 内部函数，用于动态调用 Guest 以获取表单
func (d *WasmInstance) GetFormMeta(ctx context.Context) ([]plugin_warp.FormField, error) {
	var formMeta []plugin_warp.FormField
	err := d.guest.Call(ctx, PluginPrefix+"get-form-meta", &formMeta)
	if err != nil {
		return nil, err
	}
	return formMeta, nil
}

func (i *WasmInstance) Close() error {
	return i.instance.Close(context.Background())
	// exports 借用WasmDriver的资源这里不销毁
}

// 用于创建和管理 WasmInstance
type driverPoolFactory struct {
	ctx            context.Context
	driver         *WasmDriver           // 指向 WasmDriver (状态持有者)
	compiledModule wazero.CompiledModule // 共享的模块
	runtime        wazero.Runtime        // 共享的运行时
	host           *DriverHost
}

func (f *driverPoolFactory) makeObject(ctx context.Context) (*WasmInstance, error) {
	// 1. 配置模块
	moduleConfig := wazero.NewModuleConfig().
		WithFS(os.DirFS("/")).
		WithStartFunctions("_initialize").
		WithStdout(os.Stdout).
		WithStderr(os.Stderr).
		WithStdin(os.Stdin).
		// WithSysNanosleep().
		// WithSysNanotime().
		// WithSysWalltime().
		WithOsyield(func() {
			runtime.Gosched()
		}).
		WithName(f.driver.plugin.plugin.ID)

	instanceCtx := experimental.WithMemoryAllocator(f.ctx, experimental.MemoryAllocatorFunc(alloc.NewMemory))

	// 2. 实例化共享的已编译模块
	instance, err := f.runtime.InstantiateModule(instanceCtx, f.compiledModule, moduleConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to instantiate module: %w", err)
	}

	// 3. 创建 Guest API
	guest, err := witgo.NewHost(instance)
	if err != nil {
		instance.Close(ctx)
		return nil, err
	}

	// 5. 组装 WasmInstance
	wasmInstance := &WasmInstance{
		instance: instance,
		exports:  f.host,
		guest:    guest,
	}
	return wasmInstance, nil
}

// MakeObject 创建一个新的 WasmInstance 并将其放入池中
func (f *driverPoolFactory) MakeObject(ctx context.Context) (*pool.PooledObject, error) {
	wasmInstance, err := f.makeObject(ctx)
	if err != nil {
		return nil, err
	}

	// 设置Host端句柄，用于配置获取等host端方法
	if err := wasmInstance.guest.Call(ctx, PluginPrefix+"set-handle", nil, uint32(f.driver.ID)); err != nil {
		// 这里就不返回错误了,避免大量栈数据
		log.Errorln(err)
		wasmInstance.Close()
		return nil, errors.New("Internal error in plugin")
	}

	// 调用实例的初始化方法
	ctxHandle := f.host.ContextManager().Add(ctx)
	defer f.host.ContextManager().Remove(ctxHandle)

	var result witgo.Result[witgo.Unit, plugin_warp.ErrCode]
	if err := wasmInstance.guest.Call(ctx, PluginPrefix+"init", &result, ctxHandle); err != nil {
		// 这里就不返回错误了,避免大量栈数据
		log.Errorln(err)
		wasmInstance.Close()
		return nil, errors.New("Internal error in plugin")
	}
	if result.Err != nil {
		wasmInstance.Close()
		return nil, result.Err.ToError()
	}

	return pool.NewPooledObject(wasmInstance), nil
}

// DestroyObject 销毁池中的 WasmInstance
func (f *driverPoolFactory) DestroyObject(ctx context.Context, object *pool.PooledObject) error {
	instance := object.Object.(*WasmInstance)
	log.Debugf("Destroying pooled WASM instance for plugin: %s", f.driver.Storage.MountPath)

	var err error
	// 4. 调用实例的销毁化方法
	ctxHandle := instance.exports.ContextManager().Add(ctx)
	defer instance.exports.ContextManager().Remove(ctxHandle)
	var result witgo.Result[witgo.Unit, plugin_warp.ErrCode]
	if err = instance.guest.Call(ctx, PluginPrefix+"drop", &result, ctxHandle); err != nil {
		// 这里就不返回错误了,避免大量栈数据
		log.Errorln(err)
		err = errors.New("Internal error in plugin")
	} else if result.Err != nil {
		err = result.Err.ToError()
	}

	return stderrors.Join(err, instance.Close())
}

// ValidateObject 验证实例是否仍然有效
func (f *driverPoolFactory) ValidateObject(ctx context.Context, object *pool.PooledObject) bool {
	instance := object.Object.(*WasmInstance)
	return instance.instance != nil && !instance.instance.IsClosed()
}

// ActivateObject 在借用时调用
func (f *driverPoolFactory) ActivateObject(ctx context.Context, object *pool.PooledObject) error {
	return nil
}

// PassivateObject 在归还时调用
func (f *driverPoolFactory) PassivateObject(ctx context.Context, object *pool.PooledObject) error {
	return nil
}

// WasmDriver 是*驱动*实例 (每个挂载点一个)
// 它管理池和*状态*
type WasmDriver struct {
	model.Storage
	flag uint32

	plugin *DriverPlugin

	host *DriverHost
	pool *pool.ObjectPool

	config     plugin_warp.DriverProps
	additional plugin_warp.Additional
}

// NewDriverPlugin
// 创建插件管理器
func NewDriverPlugin(ctx context.Context, plugin *PluginInfo) (*DriverPlugin, error) {
	wasmBytes, err := os.ReadFile(plugin.WasmPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read wasm file '%s': %w", plugin.WasmPath, err)
	}

	// 1. 创建共享的 wazero 运行时
	rt := wazero.NewRuntime(ctx)

	// 2. 注册 wasip1/wasip2 资源
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	host := NewDriverHost()
	if err := host.Instantiate(ctx, rt); err != nil {
		return nil, err
	}

	// 3. 编译共享的模块
	compiledModule, err := rt.CompileModule(ctx, wasmBytes)
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("failed to compile wasm module for plugin '%s': %w", plugin.ID, err)
	}

	// 4. 创建 DriverPlugin 实例（管理器）
	driverPlugin := &DriverPlugin{
		plugin:         plugin,
		runtime:        rt,
		compiledModule: compiledModule,
		host:           host,
	}
	return driverPlugin, nil
}

// Close 关闭共享的 wazero 运行时
func (dp *DriverPlugin) Close(ctx context.Context) error {
	log.Infof("Closing plugin runtime for: %s", dp.plugin.ID)
	if dp.runtime != nil {
		return dp.runtime.Close(ctx)
	}
	return nil
}

// NewWasmDriver
// 创建*驱动实例* (每个挂载一个)
func (dp *DriverPlugin) NewWasmDriver() (driver.Driver, error) {
	ctx := context.Background() // Factory/Pool context

	// 1. 创建 WasmDriver 实例 (状态持有者)
	driver := &WasmDriver{
		plugin: dp, // 指向共享资源的管理器
		host:   dp.host,
	}

	type WasmDirverWarp struct {
		*WasmDriver
	}
	driverWarp := &WasmDirverWarp{driver}
	runtime.SetFinalizer(driverWarp, func(driver *WasmDirverWarp) {
		dp.host.driver.Remove(uint32(driver.ID))
	})

	// 3. 创建池工厂
	factory := &driverPoolFactory{
		ctx:            ctx,
		driver:         driver,
		compiledModule: dp.compiledModule,
		runtime:        dp.runtime,
		host:           dp.host,
	}

	// 4. 配置并创建池
	poolConfig := pool.NewDefaultPoolConfig()
	poolConfig.MaxIdle = 2
	poolConfig.MaxTotal = 8
	poolConfig.TestOnBorrow = true
	poolConfig.BlockWhenExhausted = true
	driver.pool = pool.NewObjectPool(ctx, factory, poolConfig)

	// 5. 首次获取插件信息
	initConfig := func() error {
		instance, err := factory.makeObject(ctx)
		if err != nil {
			return err
		}
		defer instance.Close()

		props, err := instance.GetProperties(ctx)
		if err != nil {
			return fmt.Errorf("failed to refresh properties: %w", err)
		}
		driver.config = props

		forms, err := instance.GetFormMeta(ctx)
		if err != nil {
			return fmt.Errorf("failed to refresh forms: %w", err)
		}
		driver.additional.Forms = forms
		return nil
	}
	if err := initConfig(); err != nil {
		driver.Close(ctx) // 构造失败，关闭池
		return nil, err
	}
	return driverWarp, nil
}

// Close (在 WasmDriver 上) 关闭此*实例*的池
func (d *WasmDriver) Close(ctx context.Context) error {
	log.Infof("Closing pool for driver: %s", d.MountPath)
	if d.pool != nil {
		d.pool.Close(ctx)
	}
	return nil
}

// handleError 处理 wasm 驱动返回的错误
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

// // 内部函数，用于动态调用 Guest 以获取属性
// func (d *WasmDriver) getProperties(ctx context.Context) (plugin_warp.DriverProps, error) {
// 	obj, err := d.pool.BorrowObject(ctx)
// 	if err != nil {
// 		return plugin_warp.DriverProps{}, fmt.Errorf("failed to borrow wasm instance: %w", err)
// 	}
// 	instance := obj.(*WasmInstance)
// 	defer d.pool.ReturnObject(ctx, obj)

// 	return instance.GetProperties(ctx)
// }

// // 内部函数，用于动态调用 Guest 以获取表单
// func (d *WasmDriver) getFormMeta(ctx context.Context) ([]plugin_warp.FormField, error) {
// 	obj, err := d.pool.BorrowObject(ctx)
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to borrow wasm instance: %w", err)
// 	}
// 	instance := obj.(*WasmInstance)
// 	defer d.pool.ReturnObject(ctx, obj)

// 	return instance.GetFormMeta(ctx)
// }

// Config 返回缓存的配置
func (d *WasmDriver) Config() driver.Config {
	// props, err := d.getProperties(context.Background())
	// if err != nil {
	// 	log.Errorf("failed to get properties: %s", err)
	// 	return d.config.ToConfig()
	// }

	// d.config = props
	return d.config.ToConfig()
}

func (d *WasmDriver) GetAddition() driver.Additional {
	// newFormMeta, err := d.getFormMeta(context.Background())
	// if err != nil {
	// 	log.Errorf("failed to get form meta: %s", err)
	// 	return &d.additional
	// }
	// d.additional.Forms = newFormMeta
	return &d.additional
}

// Init 初始化驱动
func (d *WasmDriver) Init(ctx context.Context) error {
	log.Debugf("Re-initializing pool for plugin %s by clearing idle.", d.MountPath)
	d.pool.Clear(ctx)

	// 注册
	d.host.driver.Set(uint32(d.ID), d)

	obj, err := d.pool.BorrowObject(ctx)
	if err != nil {
		return fmt.Errorf("failed to pre-warm pool after re-init: %w", err)
	}
	d.pool.ReturnObject(ctx, obj)
	return nil
}

// Drop 销毁驱动 (由 Guest 调用)
func (d *WasmDriver) Drop(ctx context.Context) error {
	log.Infof("Guest triggered Drop, closing pool for driver: %s", d.MountPath)
	return d.Close(ctx)
}

func (d *WasmDriver) GetRoot(ctx context.Context) (model.Obj, error) {
	if !d.config.Capabilitys.ListFile {
		return nil, errs.NotImplement
	}

	obj, err := d.pool.BorrowObject(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to borrow wasm instance: %w", err)
	}
	instance := obj.(*WasmInstance)
	defer d.pool.ReturnObject(ctx, obj)

	ctxHandle := instance.exports.ContextManager().Add(ctx)
	defer instance.exports.ContextManager().Remove(ctxHandle)

	var result witgo.Result[plugin_warp.Object, plugin_warp.ErrCode]
	err = instance.guest.Call(ctx, PluginPrefix+"get-root", &result, ctxHandle)
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

	obj, err := d.pool.BorrowObject(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to borrow wasm instance: %w", err)
	}
	instance := obj.(*WasmInstance)
	defer d.pool.ReturnObject(ctx, obj)

	ctxHandle := instance.exports.ContextManager().Add(ctx)
	defer instance.exports.ContextManager().Remove(ctxHandle)

	var result witgo.Result[plugin_warp.Object, plugin_warp.ErrCode]
	err = instance.guest.Call(ctx, PluginPrefix+"get-file", &result, ctxHandle, path)
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

	obj, err := d.pool.BorrowObject(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to borrow wasm instance: %w", err)
	}
	instance := obj.(*WasmInstance)
	defer d.pool.ReturnObject(ctx, obj)

	ctxHandle := instance.exports.ContextManager().Add(ctx)
	defer instance.exports.ContextManager().Remove(ctxHandle)

	robj := dir.(*plugin_warp.Object)
	var result witgo.Result[[]plugin_warp.Object, plugin_warp.ErrCode]

	param := struct {
		Handle plugin_warp.Context
		Obj    *plugin_warp.Object
	}{ctxHandle, robj}
	err = instance.guest.Call(ctx, PluginPrefix+"list-files", &result, param)
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

	pobj, err := d.pool.BorrowObject(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to borrow wasm instance: %w", err)
	}
	instance := pobj.(*WasmInstance)
	defer d.pool.ReturnObject(ctx, pobj)

	ctxHandle := instance.exports.ContextManager().Add(ctx)
	defer instance.exports.ContextManager().Remove(ctxHandle)
	headersHandle := instance.exports.HTTPManager().Fields.Add(args.Header)
	defer instance.exports.HTTPManager().Fields.Remove(headersHandle)

	obj := file.(*plugin_warp.Object)

	var result witgo.Result[plugin_warp.LinkResult, plugin_warp.ErrCode]

	param := struct {
		Handle   plugin_warp.Context
		Obj      *plugin_warp.Object
		LinkArgs plugin_warp.LinkArgs
	}{ctxHandle, obj, plugin_warp.LinkArgs{IP: args.IP, Header: headersHandle}}
	err = instance.guest.Call(ctx, PluginPrefix+"link-file", &result, param)
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
		header, _ := instance.exports.HTTPManager().Fields.Pop(direct.Header)
		link := &model.Link{URL: direct.Url, Header: http.Header(header)}
		if direct.Expiratcion.IsSome() {
			exp := direct.Expiratcion.Some.ToDuration()
			link.Expiration = &exp
		}
		return link, nil
	}

	if result.Ok.Resource.RangeStream != nil {
		fileSize := obj.GetSize()
		return &model.Link{
			RangeReader: stream.RateLimitRangeReaderFunc(func(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
				var size uint64
				if httpRange.Length < 0 || httpRange.Start+httpRange.Length > fileSize {
					size = uint64(fileSize - httpRange.Start)
				} else {
					size = uint64(httpRange.Length)
				}

				pobj, err := d.pool.BorrowObject(ctx)
				if err != nil {
					return nil, err
				}
				instance := pobj.(*WasmInstance)

				r, w := io.Pipe()
				cw := &checkWriter{W: w, N: size}
				streamHandle := instance.exports.StreamManager().Add(&manager_io.Stream{
					Writer:      cw,
					CheckWriter: cw,
				})
				ctxHandle := instance.exports.ContextManager().Add(ctx)

				type RangeSpec struct {
					Offset uint64
					Size   uint64
					Stream io_v_0_2.OutputStream
				}

				var result witgo.Result[witgo.Unit, plugin_warp.ErrCode]
				param := struct {
					Handle    plugin_warp.Context
					Obj       *plugin_warp.Object
					LinkArgs  plugin_warp.LinkArgs
					RangeSpec RangeSpec
				}{ctxHandle, obj, plugin_warp.LinkArgs{IP: args.IP, Header: headersHandle}, RangeSpec{Offset: uint64(httpRange.Start), Size: size, Stream: streamHandle}}

				go func() {
					defer d.pool.ReturnObject(ctx, instance)
					defer instance.exports.ContextManager().Remove(ctxHandle)

					if err := instance.guest.Call(ctx, PluginPrefix+"link-range", &result, param); err != nil {
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
					instance.exports.StreamManager().Remove(streamHandle)
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

	obj, err := d.pool.BorrowObject(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to borrow wasm instance: %w", err)
	}
	instance := obj.(*WasmInstance)
	defer d.pool.ReturnObject(ctx, obj)

	ctxHandle := instance.exports.ContextManager().Add(ctx)
	defer instance.exports.ContextManager().Remove(ctxHandle)

	robj := parentDir.(*plugin_warp.Object)
	var result witgo.Result[witgo.Option[plugin_warp.Object], plugin_warp.ErrCode]

	if err := instance.guest.Call(ctx, PluginPrefix+"make-dir", &result, ctxHandle, robj, dirName); err != nil {
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

	obj, err := d.pool.BorrowObject(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to borrow wasm instance: %w", err)
	}
	instance := obj.(*WasmInstance)
	defer d.pool.ReturnObject(ctx, obj)

	ctxHandle := instance.exports.ContextManager().Add(ctx)
	defer instance.exports.ContextManager().Remove(ctxHandle)

	robj := srcObj.(*plugin_warp.Object)
	var result witgo.Result[witgo.Option[plugin_warp.Object], plugin_warp.ErrCode]

	err = instance.guest.Call(ctx, PluginPrefix+"rename-file", &result, ctxHandle, robj, newName)
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

	obj, err := d.pool.BorrowObject(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to borrow wasm instance: %w", err)
	}
	instance := obj.(*WasmInstance)
	defer d.pool.ReturnObject(ctx, obj)

	ctxHandle := instance.exports.ContextManager().Add(ctx)
	defer instance.exports.ContextManager().Remove(ctxHandle)

	srcobj := srcObj.(*plugin_warp.Object)
	dstobj := dstDir.(*plugin_warp.Object)

	var result witgo.Result[witgo.Option[plugin_warp.Object], plugin_warp.ErrCode]

	err = instance.guest.Call(ctx, PluginPrefix+"move-file", &result, ctxHandle, srcobj, dstobj)
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

	obj, err := d.pool.BorrowObject(ctx)
	if err != nil {
		return fmt.Errorf("failed to borrow wasm instance: %w", err)
	}
	instance := obj.(*WasmInstance)
	defer d.pool.ReturnObject(ctx, obj)

	ctxHandle := instance.exports.ContextManager().Add(ctx)
	defer instance.exports.ContextManager().Remove(ctxHandle)

	srcobj := srcObj.(*plugin_warp.Object)

	var result witgo.Result[witgo.Unit, plugin_warp.ErrCode]

	err = instance.guest.Call(ctx, PluginPrefix+"remove-file", &result, ctxHandle, srcobj)
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

	obj, err := d.pool.BorrowObject(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to borrow wasm instance: %w", err)
	}
	instance := obj.(*WasmInstance)
	defer d.pool.ReturnObject(ctx, obj)

	ctxHandle := instance.exports.ContextManager().Add(ctx)
	defer instance.exports.ContextManager().Remove(ctxHandle)

	srcobj := srcObj.(*plugin_warp.Object)
	dstobj := dstDir.(*plugin_warp.Object)

	var result witgo.Result[witgo.Option[plugin_warp.Object], plugin_warp.ErrCode]

	err = instance.guest.Call(ctx, PluginPrefix+"copy-file", &result, ctxHandle, srcobj, dstobj)
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

	obj, err := d.pool.BorrowObject(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to borrow wasm instance: %w", err)
	}
	instance := obj.(*WasmInstance)
	defer d.pool.ReturnObject(ctx, obj)

	ctxHandle := instance.exports.ContextManager().Add(ctx)
	defer instance.exports.ContextManager().Remove(ctxHandle)

	stream := instance.exports.uploads.Add(&plugin_warp.UploadReadableType{FileStreamer: file, UpdateProgress: up})
	defer instance.exports.uploads.Remove(stream)

	dstobj := dstDir.(*plugin_warp.Object)

	var result witgo.Result[witgo.Option[plugin_warp.Object], plugin_warp.ErrCode]

	exist := witgo.None[plugin_warp.Object]()
	if file.GetExist() != nil {
		exist = witgo.Some(plugin_warp.ConvertObjToObject(file.GetExist()))
	}

	uploadReq := &plugin_warp.UploadRequest{
		Target:  plugin_warp.ConvertObjToObject(file),
		Content: stream,
		Exist:   exist,
	}

	err = instance.guest.Call(ctx, PluginPrefix+"upload-file", &result, ctxHandle, dstobj, uploadReq)
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
