package wopi

// ActionType constants
const (
	ActionView = "view"
	ActionEdit = "edit"
)

// WopiService represents a single WOPI service configuration (e.g. Collabora, OnlyOffice)
type WopiService struct {
	Name     string                    `json:"name"`
	Endpoint string                    `json:"endpoint"`
	Viewers  map[string]WopiViewerInfo `json:"viewers,omitempty"` // ext -> viewer info
}

// WopiViewerInfo is the viewer info for a specific file extension within a service
type WopiViewerInfo struct {
	ServiceName string            `json:"service_name"`
	DisplayName string            `json:"display_name"`
	Icon        string            `json:"icon"`
	Actions     map[string]string `json:"actions"` // action_name -> base url
}

// WopiServices is the top-level type stored in the wopi_services setting
type WopiServices []WopiService

// FindViewerInService finds a viewer filtered by service name (empty = any service)
func (ws WopiServices) FindViewerInService(ext string, canEdit bool, serviceName string) (*WopiService, *WopiViewerInfo, string) {
	for i := range ws {
		svc := &ws[i]
		if serviceName != "" && svc.Name != serviceName {
			continue
		}
		info, ok := svc.Viewers[ext]
		if !ok {
			continue
		}
		if canEdit {
			if url, ok := info.Actions[ActionEdit]; ok {
				return svc, &info, url
			}
		}
		if url, ok := info.Actions[ActionView]; ok {
			return svc, &info, url
		}
	}
	return nil, nil, ""
}
