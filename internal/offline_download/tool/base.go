package tool

import (
	"context"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type AddUrlArgs struct {
	Url         string
	TorrentData []byte
	UID         string
	TempDir     string
	Signal      chan int
	Ctx         context.Context
}

type Status struct {
	TotalBytes int64
	Progress   float64
	NewGID     string
	Completed  bool
	Status     string
	Err        error
}

// Capabilities describes optional input features supported by an offline download tool.
// The zero value represents a tool with no optional capabilities.
type Capabilities struct {
	TorrentData bool
}

// CapabilityProvider is implemented by tools that support optional capabilities.
type CapabilityProvider interface {
	Capabilities() Capabilities
}

// CapabilitiesOf returns the optional capabilities advertised by a tool.
func CapabilitiesOf(downloadTool Tool) Capabilities {
	provider, ok := downloadTool.(CapabilityProvider)
	if !ok {
		return Capabilities{}
	}
	return provider.Capabilities()
}

type Tool interface {
	Name() string
	// Items return the setting items the tool need
	Items() []model.SettingItem
	Init() (string, error)
	IsReady() bool
	// AddURL add an uri to download, return the task id
	AddURL(args *AddUrlArgs) (string, error)
	// Remove the download if task been canceled
	Remove(task *DownloadTask) error
	// Status return the status of the download task, if an error occurred, return the error in Status.Err
	Status(task *DownloadTask) (*Status, error)

	// Run for simple http download
	Run(task *DownloadTask) error
}
