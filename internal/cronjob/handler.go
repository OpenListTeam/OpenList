// Package cronjob 是通用计划任务模块。
// 设计目标是先建立统一的 cron 调度和任务注册能力，再把同步、复制、删除、
// 重新加载存储等不同任务类型作为 Handler 挂载进来，避免后续功能重复造调度轮子。
package cronjob

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Handler 是某种计划任务类型的执行契约。
// 参数以 json.RawMessage 传递，使每种任务类型可以定义自己的配置结构，
// 调度器与数据库模型无需随着新增任务类型而修改字段。
type Handler interface {
	// ValidateArgs 校验某种任务类型的参数；必须保证 API 创建前也能调用。
	ValidateArgs(args json.RawMessage) error
	// Run 执行一次任务；返回 error 后调度器会把它记录到 last_error。
	Run(ctx context.Context, args json.RawMessage) error
}

// ArgFieldType 描述前端应该渲染哪一种控件。
// 这里刻意使用简单字符串而不是 JSON Schema：驱动未来注册任务时只需给出少量元数据，
// 不需要引入额外的 schema 库，前端也能直接复用现有 Input/Textarea/Switch/路径选择控件。
type ArgFieldType string

const (
	// ArgFieldTypeString 渲染普通单行输入框。
	ArgFieldTypeString ArgFieldType = "string"
	// ArgFieldTypeText 渲染多行文本框；值仍以字符串保存。
	ArgFieldTypeText ArgFieldType = "text"
	// ArgFieldTypeLines 渲染多行文本框，但提交时转换为 []string。
	ArgFieldTypeLines ArgFieldType = "lines"
	// ArgFieldTypeNumber 渲染数字输入框；值以 number 保存。
	ArgFieldTypeNumber ArgFieldType = "number"
	// ArgFieldTypeBool 渲染开关；值以 boolean 保存。
	ArgFieldTypeBool ArgFieldType = "bool"
	// ArgFieldTypePath 渲染 OpenList 虚拟路径选择输入框。
	ArgFieldTypePath ArgFieldType = "path"
)

// ArgField 描述一个任务参数字段。
// LabelKey/HelpKey 使用前端 i18n key，避免后端硬编码 UI 文案。
type ArgField struct {
	// Name 与任务 Args JSON 中的 key 一致。
	Name string `json:"name"`
	// Type 决定前端渲染的控件类型。
	Type ArgFieldType `json:"type"`
	// LabelKey 是字段名称的 i18n key。
	LabelKey string `json:"label_key"`
	// HelpKey 是字段说明的 i18n key；空字符串表示没有说明。
	HelpKey string `json:"help_key"`
	// Required 表示创建/编辑时前端是否必须填写。
	Required bool `json:"required"`
	// Default 是前端初始值；统一用字符串表示，由前端按 Type 转换。
	Default string `json:"default"`
}

// HandlerInfo 描述一种计划任务类型。
// 前端不硬编码任务类型，而是通过 API 拉取该列表并动态渲染设置项。
type HandlerInfo struct {
	// Type 是创建 CronJob 时使用的稳定任务类型标识，例如 sync。
	Type string `json:"type"`
	// LabelKey 是任务类型显示名称的 i18n key。
	LabelKey string `json:"label_key"`
	// DescriptionKey 是任务类型说明的 i18n key。
	DescriptionKey string `json:"description_key"`
	// Fields 描述该任务类型的所有参数。
	Fields []ArgField `json:"fields"`
}

// InfoProvider 是可选扩展接口。
// 只有实现了该接口的 Handler 才会向前端暴露表单描述；
// 后台任务仍可以只实现 Handler，用于不允许/不需要管理员配置的内部场景。
type InfoProvider interface {
	// HandlerInfo 返回给前端展示的类型描述。
	HandlerInfo() HandlerInfo
}

// DescribedHandler 把执行逻辑和表单描述绑定在一起。
// 这样新增任务类型时只需要注册一个对象，不用维护两份注册表。
type DescribedHandler struct {
	// HandlerFunc 提供 ValidateArgs 和 Run。
	HandlerFunc
	// Info 是该任务类型的表单描述。
	Info HandlerInfo
}

// HandlerInfo 返回绑定到当前 Handler 的类型描述。
func (h DescribedHandler) HandlerInfo() HandlerInfo {
	return h.Info
}

// HandlerFunc 将两个函数包装为 Handler，便于只实现一个执行逻辑的内置类型使用。
type HandlerFunc struct {
	// ValidateFunc 不能为空，用于创建/更新前的参数校验。
	ValidateFunc func(args json.RawMessage) error
	// RunFunc 不能为空，表示一次计划触发。
	RunFunc func(ctx context.Context, args json.RawMessage) error
}

// ValidateArgs 调用包装的校验函数。
func (h HandlerFunc) ValidateArgs(args json.RawMessage) error {
	if h.ValidateFunc == nil {
		return fmt.Errorf("handler validate function is nil")
	}
	return h.ValidateFunc(args)
}

// Run 调用包装的执行函数。
func (h HandlerFunc) Run(ctx context.Context, args json.RawMessage) error {
	if h.RunFunc == nil {
		return fmt.Errorf("handler run function is nil")
	}
	return h.RunFunc(ctx, args)
}

// registry 保存全局任务类型注册表。
// RegisterHandler 与 LookupHandler 都使用这把锁保护，支持后续驱动注册任务类型。
var registry = struct {
	sync.RWMutex
	handlers map[string]Handler
}{
	handlers: make(map[string]Handler),
}

// RegisterHandler 注册一种计划任务类型。
// 重复注册直接 panic：这类内置注册错误应该在启动阶段立刻暴露。
func RegisterHandler(jobType string, handler Handler) {
	if jobType == "" {
		panic("cronjob type must not be empty")
	}
	if handler == nil {
		panic("cronjob handler must not be nil")
	}
	registry.Lock()
	defer registry.Unlock()
	if _, exists := registry.handlers[jobType]; exists {
		panic(fmt.Sprintf("cronjob handler %q already registered", jobType))
	}
	registry.handlers[jobType] = handler
}

// LookupHandler 按任务类型查找 Handler；不存在时返回错误而不是 nil。
func LookupHandler(jobType string) (Handler, error) {
	registry.RLock()
	defer registry.RUnlock()
	handler, ok := registry.handlers[jobType]
	if !ok {
		return nil, fmt.Errorf("unknown cronjob type: %s", jobType)
	}
	return handler, nil
}

// RegisteredTypes 返回当前可用的任务类型，供 API 或前端展示使用。
func RegisteredTypes() []string {
	registry.RLock()
	defer registry.RUnlock()
	types := make([]string, 0, len(registry.handlers))
	for jobType := range registry.handlers {
		types = append(types, jobType)
	}
	return types
}

// RegisteredHandlerInfos 返回所有实现了 InfoProvider 的任务类型描述。
// 结果按 Type 排序，保证前端下拉框顺序稳定。
func RegisteredHandlerInfos() []HandlerInfo {
	registry.RLock()
	defer registry.RUnlock()

	infos := make([]HandlerInfo, 0, len(registry.handlers))
	for _, handler := range registry.handlers {
		provider, ok := handler.(InfoProvider)
		if !ok {
			// 没有描述的任务不出现在前端创建下拉框中，但依然可以被调度器执行。
			continue
		}
		infos = append(infos, provider.HandlerInfo())
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Type < infos[j].Type
	})
	return infos
}
