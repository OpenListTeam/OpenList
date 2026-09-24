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
)

// Export target MIME types.
const (
	mimeTypeDocx       = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	mimeTypeXlsx       = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	mimeTypePptx       = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	mimeTypePDF        = "application/pdf"
	mimeTypeScriptJSON = "application/vnd.google-apps.script+json"
)

// exportFormat holds the export target MIME type and the resulting file extension.
type exportFormat struct {
	MIME string
	Ext  string
}

// googleWorkspaceExports maps exportable Google Workspace source MIME types to their export targets.
var googleWorkspaceExports = map[string]exportFormat{
	mimeTypeGoogleDoc:     {MIME: mimeTypeDocx, Ext: ".docx"},
	mimeTypeGoogleSheet:   {MIME: mimeTypeXlsx, Ext: ".xlsx"},
	mimeTypeGoogleSlides:  {MIME: mimeTypePptx, Ext: ".pptx"},
	mimeTypeGoogleDrawing: {MIME: mimeTypePDF, Ext: ".pdf"},
	mimeTypeGoogleScript:  {MIME: mimeTypeScriptJSON, Ext: ".json"},
}

// googleWorkspaceUnsupported lists Google Workspace MIME types that cannot be downloaded
// via files.get or files.export, with a human-readable reason.
var googleWorkspaceUnsupported = map[string]string{
	mimeTypeGoogleFolder:   "folders cannot be downloaded",
	mimeTypeGoogleShortcut: "shortcuts must be resolved before downloading",
	mimeTypeGoogleForm:     "Google Forms are not downloadable via the Drive API",
	mimeTypeGoogleSite:     "Google Sites are not downloadable via the Drive API",
	mimeTypeGoogleMap:      "Google Maps are not downloadable via the Drive API",
	// Google Vids require the files.download long-running-operation flow which is not
	// yet supported. See https://developers.google.com/drive/api/reference/rest/v3/files/download
	mimeTypeGoogleVid: "Google Vids require the Drive files.download long-running-operation flow which is not yet implemented",
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
