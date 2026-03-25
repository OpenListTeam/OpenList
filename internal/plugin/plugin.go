package plugin

type Plugin interface {
	Name() string
	Init(config map[string]any) error
	Start() error
	Stop() error
}
