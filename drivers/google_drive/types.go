package google_drive

import (
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

// Google Workspace source MIME types.
const (
	mimeTypeGoogleDoc      = "application/vnd.google-apps.document"
	mimeTypeGoogleSheet    = "application/vnd.google-apps.spreadsheet"
	mimeTypeGoogleSlides   = "application/vnd.google-apps.presentation"
	mimeTypeGoogleDrawing  = "application/vnd.google-apps.drawing"
	mimeTypeGoogleScript   = "application/vnd.google-apps.script"
	mimeTypeGoogleFolder   = "application/vnd.google-apps.folder"
	mimeTypeGoogleShortcut = "application/vnd.google-apps.shortcut"
	mimeTypeGoogleForm     = "application/vnd.google-apps.form"
	mimeTypeGoogleSite     = "application/vnd.google-apps.site"
	mimeTypeGoogleMap      = "application/vnd.google-apps.map"
	mimeTypeGoogleVid      = "application/vnd.google-apps.vid"
	mimeTypeGoogleJam      = "application/vnd.google-apps.jam"
)

// Export target MIME types.
const (
	mimeTypeDocx       = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	mimeTypeXlsx       = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	mimeTypePptx       = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	mimeTypePDF        = "application/pdf"
	mimeTypeScriptJSON = "application/vnd.google-apps.script+json"
)

// googleWorkspaceExports maps exportable Google Workspace source MIME types to the
// corresponding files.export target MIME type.
var googleWorkspaceExports = map[string]string{
	mimeTypeGoogleDoc:     mimeTypeDocx,
	mimeTypeGoogleSheet:   mimeTypeXlsx,
	mimeTypeGoogleSlides:  mimeTypePptx,
	mimeTypeGoogleDrawing: mimeTypePDF,
	mimeTypeGoogleScript:  mimeTypeScriptJSON,
}

// googleWorkspaceUnsupported lists Google Workspace MIME types not handled by this driver,
// with a concise reason describing the implementation limitation.
var googleWorkspaceUnsupported = map[string]string{
	mimeTypeGoogleFolder:   "folders cannot be downloaded",
	mimeTypeGoogleShortcut: "shortcuts must be resolved before downloading",
	// Forms and Sites are documented as downloadable via the files.download LRO flow,
	// which this driver does not implement.
	mimeTypeGoogleForm: "not supported by this driver's synchronous export path; files.download LRO is not implemented",
	mimeTypeGoogleSite: "not supported by this driver's synchronous export path; files.download LRO is not implemented",
	// Google My Maps has no documented synchronous download/export path.
	mimeTypeGoogleMap: "no supported download/export strategy is implemented for Google My Maps",
	// Google Vids and Jamboard require the files.download long-running-operation flow.
	// See https://developers.google.com/drive/api/reference/rest/v3/files/download
	mimeTypeGoogleVid: "requires the files.download long-running-operation flow which is not implemented",
	mimeTypeGoogleJam: "requires the files.download long-running-operation flow which is not implemented",
}

// FileMeta holds the fields we need from a files.get metadata response inside Link().
type FileMeta struct {
	MimeType     string `json:"mimeType"`
	Capabilities struct {
		CanDownload bool `json:"canDownload"`
	} `json:"capabilities"`
}

type TokenError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type Files struct {
	NextPageToken string `json:"nextPageToken"`
	Files         []File `json:"files"`
}

type File struct {
	Id              string    `json:"id"`
	Name            string    `json:"name"`
	MimeType        string    `json:"mimeType"`
	ModifiedTime    time.Time `json:"modifiedTime"`
	CreatedTime     time.Time `json:"createdTime"`
	Size            string    `json:"size"`
	ThumbnailLink   string    `json:"thumbnailLink"`
	ShortcutDetails struct {
		TargetId       string `json:"targetId"`
		TargetMimeType string `json:"targetMimeType"`
	} `json:"shortcutDetails"`

	MD5Checksum    string `json:"md5Checksum"`
	SHA1Checksum   string `json:"sha1Checksum"`
	SHA256Checksum string `json:"sha256Checksum"`
}

func fileToObj(f File) *model.ObjThumb {
	log.Debugf("google file: %+v", f)
	size, _ := strconv.ParseInt(f.Size, 10, 64)
	obj := &model.ObjThumb{
		Object: model.Object{
			ID:       f.Id,
			Name:     f.Name,
			Size:     size,
			Ctime:    f.CreatedTime,
			Modified: f.ModifiedTime,
			IsFolder: f.MimeType == "application/vnd.google-apps.folder",
			HashInfo: utils.NewHashInfoByMap(map[*utils.HashType]string{
				utils.MD5:    f.MD5Checksum,
				utils.SHA1:   f.SHA1Checksum,
				utils.SHA256: f.SHA256Checksum,
			}),
		},
		Thumbnail: model.Thumbnail{
			Thumbnail: f.ThumbnailLink,
		},
	}
	if f.MimeType == "application/vnd.google-apps.shortcut" {
		obj.ID = f.ShortcutDetails.TargetId
		obj.IsFolder = f.ShortcutDetails.TargetMimeType == "application/vnd.google-apps.folder"
	}
	return obj
}

type Error struct {
	Error struct {
		Errors []struct {
			Domain       string `json:"domain"`
			Reason       string `json:"reason"`
			Message      string `json:"message"`
			LocationType string `json:"location_type"`
			Location     string `json:"location"`
		}
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type AboutResp struct {
	StorageQuota struct {
		Limit             *string `json:"limit"`
		Usage             string  `json:"usage"`
		UsageInDrive      string  `json:"usageInDrive"`
		UsageInDriveTrash string  `json:"usageInDriveTrash"`
	}
}
