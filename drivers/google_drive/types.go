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
	// Third-party shortcut created by a Drive app; the app must handle the download.
	mimeTypeGoogleDriveSDK = "application/vnd.google-apps.drive-sdk"
)

// Export target MIME types.
const (
	mimeTypeDocx       = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	mimeTypeXlsx       = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	mimeTypePptx       = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	mimeTypePDF        = "application/pdf"
	mimeTypeScriptJSON = "application/vnd.google-apps.script+json"
)

// exportFormat pairs a files.export target MIME type with the file extension
// that should be appended to the downloaded filename.
type exportFormat struct {
	MIME      string
	Extension string
}

// googleWorkspaceExports maps exportable Google Workspace source MIME types to their
// files.export target format.
var googleWorkspaceExports = map[string]exportFormat{
	mimeTypeGoogleDoc:     {MIME: mimeTypeDocx, Extension: ".docx"},
	mimeTypeGoogleSheet:   {MIME: mimeTypeXlsx, Extension: ".xlsx"},
	mimeTypeGoogleSlides:  {MIME: mimeTypePptx, Extension: ".pptx"},
	mimeTypeGoogleDrawing: {MIME: mimeTypePDF, Extension: ".pdf"},
	mimeTypeGoogleScript:  {MIME: mimeTypeScriptJSON, Extension: ".json"},
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
	mimeTypeGoogleVid:      "requires the files.download long-running-operation flow which is not implemented",
	mimeTypeGoogleJam:      "requires the files.download long-running-operation flow which is not implemented",
	mimeTypeGoogleDriveSDK: "third-party shortcut (drive-sdk) cannot be downloaded directly",
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
