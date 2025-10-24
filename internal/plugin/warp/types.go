package plugin_warp

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	clocks "github.com/OpenListTeam/wazero-wasip2/wasip2/clocks/v0_2"
	wasi_http "github.com/OpenListTeam/wazero-wasip2/wasip2/http/v0_2"
	witgo "github.com/OpenListTeam/wazero-wasip2/wit-go"
)

type LogLevel struct {
	Debug *struct{} `wit:"case(0)"`
	Info  *struct{} `wit:"case(1)"`
	Warn  *struct{} `wit:"case(2)"`
	Error *struct{} `wit:"case(3)"`
}

type HashAlg struct {
	MD5    *struct{} `wit:"case(0)"`
	SHA1   *struct{} `wit:"case(1)"`
	SHA256 *struct{} `wit:"case(2)"`
	GCID   *struct{} `wit:"case(3)"`
}

type HashInfo struct {
	Alg HashAlg
	Val string
}

type LinkResult struct {
	File     witgo.Option[Object]
	Resource LinkResource
}
type LinkResource struct {
	Direct *struct {
		Url         string
		Header      wasi_http.Headers
		Expiratcion witgo.Option[clocks.Duration]
	} `wit:"case(0)"`
	RangeStream *struct{} `wit:"case(1)"`
}

type Capability struct {
	GetFile    bool
	ListFile   bool
	LinkFile   bool
	MkdirFile  bool
	RenameFile bool
	MoveFile   bool
	RemoveFile bool
	CopyFile   bool
	UploadFile bool
}

func (Capability) IsFlags() {}

type DriverProps struct {
	Name string

	OnlyProxy bool
	NoCache   bool

	Alert string

	NoOverwriteUpload bool
	ProxyRange        bool

	// 网盘能力标记
	Capabilitys Capability
}

func (c DriverProps) ToConfig() driver.Config {
	return driver.Config{
		Name:      c.Name,
		LocalSort: true,
		OnlyProxy: c.OnlyProxy,
		NoCache:   c.NoCache,
		NoUpload:  !c.Capabilitys.UploadFile,

		CheckStatus: true,
		Alert:       c.Alert,

		NoOverwriteUpload: c.NoOverwriteUpload,
		ProxyRangeOption:  c.ProxyRange,
	}
}

type FormField struct {
	// 字段的唯一标识符（键）。
	Name string
	// 显示给用户的标签。
	Label string
	// 字段的输入类型，用于 UI 渲染。
	Kind FieldKind
	// 是否必填
	Required bool
	// 字段的帮助或提示信息。
	Help string
}

type FieldKind struct {
	String   *string   `wit:"case(0)"`
	Password *string   `wit:"case(1)"`
	Number   *float64  `wit:"case(2)"`
	Boolean  *bool     `wit:"case(3)"`
	Text     *string   `wit:"case(4)"`
	Select   *[]string `wit:"case(5)"`
}

type Additional struct {
	Json  []byte
	Forms []FormField
}

func NewAdditional(forms []FormField) Additional {
	return Additional{
		Forms: forms,
	}
}

func (m *Additional) String() string {
	return string(m.Json)
}
func (m *Additional) SetString(config string) {
	m.Json = []byte(config)
}

func (m *Additional) Bytes() []byte {
	return m.Json
}

func (m *Additional) SetBytes(config []byte) {
	m.Json = config
}

// MarshalJSON returns m as the JSON encoding of m.
func (m Additional) MarshalJSON() ([]byte, error) {
	return m.Json, nil
}

// UnmarshalJSON sets *m to a copy of data.
func (m *Additional) UnmarshalJSON(data []byte) error {
	if m == nil {
		return errors.New("json.RawMessage: UnmarshalJSON on nil pointer")
	}
	m.Json = slices.Clone(data)
	return nil
}

func (addit *Additional) GetItems() []driver.Item {
	return utils.MustSliceConvert(addit.Forms, func(item FormField) driver.Item {
		var typ string
		var def string
		var opts string
		if item.Kind.Boolean != nil {
			typ = conf.TypeBool
			def = fmt.Sprintf("%t", *item.Kind.Boolean)
		} else if item.Kind.Password != nil {
			typ = conf.TypeString
			def = *item.Kind.Password
		} else if item.Kind.Number != nil {
			typ = conf.TypeNumber
			def = fmt.Sprintf("%f", *item.Kind.Number)
		} else if item.Kind.Select != nil {
			typ = conf.TypeSelect
			if len(*item.Kind.Select) > 0 {
				def = (*item.Kind.Select)[0]
				opts = strings.Join((*item.Kind.Select), ",")
			}
		} else if item.Kind.String != nil {
			typ = conf.TypeString
			def = *item.Kind.String
		} else if item.Kind.Text != nil {
			typ = conf.TypeText
			def = *item.Kind.Text
		}

		return driver.Item{
			Name:     item.Name,
			Type:     typ,
			Default:  def,
			Options:  opts,
			Required: item.Required,
			Help:     item.Help,
		}
	})
}

type LinkArgs struct {
	IP     string
	Header wasi_http.Headers
}
