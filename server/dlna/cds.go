//go:build go1.21

package dlna

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/internal/fs"
	"github.com/OpenListTeam/OpenList/internal/model"
	"github.com/OpenListTeam/OpenList/server/dlna/upnpav"
	"github.com/anacrolix/dms/dlna"
	"github.com/anacrolix/dms/upnp"
	log "github.com/sirupsen/logrus"
)

type contentDirectoryService struct {
	*server
	upnp.Eventing
}

func (cds *contentDirectoryService) updateIDString() string {
	return fmt.Sprintf("%d", uint32(os.Getpid()))
}

var mediaMimeTypeRegexp = regexp.MustCompile("^(video|audio|image)/")

// MimeTypeFromName returns a guess at the mime type from the name
func MimeTypeFromName(remote string) (mimeType string) {
	mimeType = mime.TypeByExtension(path.Ext(remote))
	if !strings.ContainsRune(mimeType, '/') {
		mimeType = "application/octet-stream"
	}
	return mimeType
}

// Turns the given entry and DMS host into a UPnP object. A nil object is
// returned if the entry is not of interest.
func (cds *contentDirectoryService) cdsObjectToUpnpavObject(cdsObject object, fileInfo model.Obj, resources []model.Obj, host string) (ret interface{}, err error) {
	obj := upnpav.Object{
		ID:         cdsObject.ID(),
		Restricted: 1,
		ParentID:   cdsObject.ParentID(),
	}

	if fileInfo.IsDir() {
		defaultChildCount := 1
		obj.Class = "object.container.storageFolder"
		obj.Title = fileInfo.GetName()
		return upnpav.Container{
			Object:     obj,
			ChildCount: &defaultChildCount,
		}, nil
	}

	mimeType := MimeTypeFromName(fileInfo.GetName())

	mediaType := mediaMimeTypeRegexp.FindStringSubmatch(mimeType)
	if mediaType == nil {
		return
	}

	obj.Class = "object.item." + mediaType[1] + "Item"
	obj.Title = fileInfo.GetName()
	obj.Date = upnpav.Timestamp{Time: fileInfo.ModTime()}

	item := upnpav.Item{
		Object: obj,
		Res:    make([]upnpav.Resource, 0, 1),
	}

	item.Res = append(item.Res, upnpav.Resource{
		URL: (&url.URL{
			Scheme: "http",
			Host:   host,
			Path:   path.Join(resPath, cdsObject.Path),
		}).String(),
		ProtocolInfo: fmt.Sprintf("http-get:*:%s:%s", mimeType, dlna.ContentFeatures{
			SupportRange: true,
		}.String()),
		Size: uint64(fileInfo.GetSize()),
	})

	for _, resource := range resources {
		subtitleURL := (&url.URL{
			Scheme: "http",
			Host:   host,
			Path:   path.Join(resPath, resource.GetPath()),
		}).String()
		item.Res = append(item.Res, upnpav.Resource{
			URL:          subtitleURL,
			ProtocolInfo: fmt.Sprintf("http-get:*:%s:*", "text/srt"),
		})
	}

	ret = item
	return
}

// Returns all the upnpav objects in a directory.
func (cds *contentDirectoryService) readContainer(o object, host string) (ret []interface{}, err error) {
	node, err := fs.Get(context.Background(), cds.RootDir+o.Path, &fs.GetArgs{})
	if err != nil {
		return
	}

	if !node.IsDir() {
		err = errors.New("not a directory")
		return
	}

	dirEntries, err := fs.List(context.Background(), cds.RootDir+o.Path, &fs.ListArgs{})
	if err != nil {
		err = errors.New("failed to list directory")
		return
	}

	// Sort the directory entries by directories first then alphabetically by name
	sort.Slice(dirEntries, func(i, j int) bool {
		iNode, jNode := dirEntries[i], dirEntries[j]
		iIsDir, jIsDir := iNode.IsDir(), jNode.IsDir()
		if iIsDir && !jIsDir {
			return true
		} else if !iIsDir && jIsDir {
			return false
		}
		return strings.ToLower(iNode.GetName()) < strings.ToLower(jNode.GetName())
	})

	dirEntries, mediaResources := mediaWithResources(dirEntries)
	for _, de := range dirEntries {
		child := object{
			path.Join(o.Path, de.GetName()),
		}
		obj, err := cds.cdsObjectToUpnpavObject(child, de, mediaResources[de], host)
		if err != nil {
			log.Errorf("error with %s: %s", child.FilePath(), err)
			continue
		}
		if obj == nil {
			log.Debugf("unrecognized file type: %s", de)
			continue
		}
		ret = append(ret, obj)
	}

	return
}

// Given a list of nodes, separate them into potential media items and any associated resources (external subtitles,
// for example.)
//
// The result is a slice of potential media nodes (in their original order) and a map containing associated
// resources nodes of each media node, if any.
func mediaWithResources(nodes []model.Obj) ([]model.Obj, map[model.Obj][]model.Obj) {
	media, mediaResources := make([]model.Obj, 0), make(map[model.Obj][]model.Obj)

	// First, separate out the subtitles and media into maps, keyed by their lowercase base names.
	mediaByName, subtitlesByName := make(map[string][]model.Obj), make(map[string]model.Obj)
	for _, node := range nodes {
		baseName, ext := splitExt(strings.ToLower(node.GetName()))
		switch ext {
		case ".srt", ".ass", ".ssa", ".sub", ".idx", ".sup", ".jss", ".txt", ".usf", ".cue", ".vtt", ".css":
			// .idx should be with .sub, .css should be with vtt otherwise they should be culled,
			// and their mimeTypes are not consistent, but anyway these negatives don't throw errors.
			subtitlesByName[baseName] = node
		default:
			mediaByName[baseName] = append(mediaByName[baseName], node)
			media = append(media, node)
		}
	}

	// Find the associated media file for each subtitle
	for baseName, node := range subtitlesByName {
		// Find a media file with the same basename (video.mp4 for video.srt)
		mediaNodes, found := mediaByName[baseName]
		if !found {
			// Or basename of the basename (video.mp4 for video.en.srt)
			baseName, _ = splitExt(baseName)
			mediaNodes, found = mediaByName[baseName]
		}

		// Just advise if no match found
		if !found {
			log.Infof("could not find associated media for subtitle: %s", node.GetName())
			continue
		}

		// Associate with all potential media nodes
		log.Debugf("associating subtitle: %s", node.GetName())
		for _, mediaNode := range mediaNodes {
			mediaResources[mediaNode] = append(mediaResources[mediaNode], node)
		}
	}

	return media, mediaResources
}

type browse struct {
	ObjectID       string
	BrowseFlag     string
	Filter         string
	StartingIndex  int
	RequestedCount int
}

// ContentDirectory object from ObjectID.
func (cds *contentDirectoryService) objectFromID(id string) (o object, err error) {
	o.Path, err = url.QueryUnescape(id)
	if err != nil {
		return
	}
	if o.Path == "0" {
		o.Path = "/"
	}
	o.Path = path.Clean(o.Path)
	if !path.IsAbs(o.Path) {
		err = fmt.Errorf("bad ObjectID %v", o.Path)
		return
	}
	return
}

var _OnLastHandleGetSearchCapabilitiesTime int64 = 0

func (cds *contentDirectoryService) Handle(action string, argsXML []byte, r *http.Request) (map[string]string, error) {
	host := r.Host

	switch action {
	case "GetSystemUpdateID":
		return map[string]string{
			"Id": cds.updateIDString(),
		}, nil
	case "GetSortCapabilities":
		return map[string]string{
			"SortCaps": "dc:title",
		}, nil
	case "Browse":
		var browse browse
		if err := xml.Unmarshal(argsXML, &browse); err != nil {
			return nil, err
		}
		obj, err := cds.objectFromID(browse.ObjectID)
		if err != nil {
			return nil, upnp.Errorf(upnpav.NoSuchObjectErrorCode, "%s", err.Error())
		}
		switch browse.BrowseFlag {
		case "BrowseDirectChildren":
			var objs []interface{}
			if _OnLastHandleGetSearchCapabilitiesTime == 0 || time.Now().UnixMilli()-_OnLastHandleGetSearchCapabilitiesTime >= 8000 {
				var err error
				objs, err = cds.readContainer(obj, host)
				if err != nil {
					return nil, upnp.Errorf(upnpav.NoSuchObjectErrorCode, "%s", err.Error())
				}
			} else {
				log.Infof("Detected webOS TV starting disk scan, returned empty folder")
			}
			_OnLastHandleGetSearchCapabilitiesTime = 0

			totalMatches := len(objs)
			objs = objs[func() (low int) {
				low = min(browse.StartingIndex, len(objs))
				return
			}():]
			if browse.RequestedCount != 0 && browse.RequestedCount < len(objs) {
				objs = objs[:browse.RequestedCount]
			}
			result, err := xml.Marshal(objs)
			if err != nil {
				return nil, err
			}
			return map[string]string{
				"TotalMatches":   fmt.Sprint(totalMatches),
				"NumberReturned": fmt.Sprint(len(objs)),
				"Result":         didlLite(string(result)),
				"UpdateID":       cds.updateIDString(),
			}, nil
		case "BrowseMetadata":
			node, err := fs.Get(context.Background(), obj.Path, &fs.GetArgs{})
			if err != nil {
				return nil, err
			}
			// TODO: External subtitles won't appear in the metadata here, but probably should.
			upnpObject, err := cds.cdsObjectToUpnpavObject(obj, node, []model.Obj{}, host)
			if err != nil {
				return nil, err
			}
			result, err := xml.Marshal(upnpObject)
			if err != nil {
				return nil, err
			}
			return map[string]string{
				"TotalMatches":   "1",
				"NumberReturned": "1",
				"Result":         didlLite(string(result)),
				"UpdateID":       cds.updateIDString(),
			}, nil
		default:
			return nil, upnp.Errorf(upnp.ArgumentValueInvalidErrorCode, "unhandled browse flag: %v", browse.BrowseFlag)
		}
	case "GetSearchCapabilities":
		_OnLastHandleGetSearchCapabilitiesTime = time.Now().UnixMilli()
		return map[string]string{
			"SearchCaps": "",
		}, nil
	// Samsung Extensions
	case "X_GetFeatureList":
		return map[string]string{
			"FeatureList": `<Features xmlns="urn:schemas-upnp-org:av:avs" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:schemaLocation="urn:schemas-upnp-org:av:avs http://www.upnp.org/schemas/av/avs.xsd">
	<Feature name="samsung.com_BASICVIEW" version="1">
		<container id="0" type="object.item.imageItem"/>
		<container id="0" type="object.item.audioItem"/>
		<container id="0" type="object.item.videoItem"/>
	</Feature>
</Features>`}, nil
	case "X_SetBookmark":
		// just ignore
		return map[string]string{}, nil
	default:
		return nil, upnp.InvalidActionError
	}
}

// Represents a ContentDirectory object.
type object struct {
	Path string // The cleaned, absolute path for the object relative to the server.
}

// Returns the actual local filesystem path for the object.
func (o *object) FilePath() string {
	return filepath.FromSlash(o.Path)
}

// Returns the ObjectID for the object. This is used in various ContentDirectory actions.
func (o object) ID() string {
	if !path.IsAbs(o.Path) {
		log.Panicf("Relative object path: %s", o.Path)
	}
	if len(o.Path) == 1 {
		return "0"
	}
	return url.QueryEscape(o.Path)
}

func (o *object) IsRoot() bool {
	return o.Path == "/"
}

// Returns the object's parent ObjectID. Fortunately it can be deduced from the
// ObjectID (for now).
func (o object) ParentID() string {
	if o.IsRoot() {
		return "-1"
	}
	o.Path = path.Dir(o.Path)
	return o.ID()
}
