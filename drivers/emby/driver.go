package emby

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Emby struct {
	model.Storage
	Addition

	client *http.Client
	token  string
	userID string
}

func (d *Emby) Config() driver.Config {
	return config
}

func (d *Emby) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Emby) Init(ctx context.Context) error {
	d.URL = strings.TrimRight(strings.TrimSpace(d.URL), "/")
	if d.URL == "" {
		return fmt.Errorf("url is required")
	}

	if strings.TrimSpace(d.RootFolderID) == "" {
		d.RootFolderID = "1"
	}

	d.client = base.HttpClient
	d.token = strings.TrimSpace(d.ApiKey)
	d.userID = strings.TrimSpace(d.UserID)

	if d.token != "" {
		if d.userID == "" {
			return fmt.Errorf("user_id is required when api_key is set")
		}
		op.MustSaveDriverStorage(d)
		return nil
	}

	if strings.TrimSpace(d.Username) == "" || strings.TrimSpace(d.Password) == "" {
		return fmt.Errorf("please provide api_key+user_id or username+password")
	}

	if err := d.login(ctx); err != nil {
		return err
	}

	d.ApiKey = d.token
	d.UserID = d.userID
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *Emby) Drop(ctx context.Context) error {
	return nil
}

func (d *Emby) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	parentID := strings.TrimSpace(d.RootFolderID)
	if dir != nil && strings.TrimSpace(dir.GetID()) != "" {
		parentID = strings.TrimSpace(dir.GetID())
	}

	items, err := d.getItems(ctx, parentID)
	if err != nil {
		return nil, err
	}

	parentPath := "/"
	if dir != nil && strings.TrimSpace(dir.GetPath()) != "" {
		parentPath = dir.GetPath()
	}

	objs := make([]model.Obj, 0, len(items.Items))
	for _, it := range items.Items {
		modified := time.Now()
		if it.DateCreated != "" {
			if t, parseErr := time.Parse(time.RFC3339Nano, it.DateCreated); parseErr == nil {
				modified = t
			}
		}

		name := strings.TrimSpace(it.Name)
		id := strings.TrimSpace(it.ID)
		displayName := name
		if name != "" && id != "" {
			if it.IsFolder {
				displayName = fmt.Sprintf("%s (ID%s)", name, id)
			} else {
				ext := path.Ext(strings.TrimSpace(it.Path))
				if ext == "" {
					ext = path.Ext(name)
				}

				base := strings.TrimSpace(strings.TrimSuffix(name, ext))
				episodeCode := ""
				if m := episodeCodeRegexp.FindString(base); m != "" {
					episodeCode = strings.ToUpper(m)
				} else if it.ParentIndex > 0 && it.IndexNumber > 0 {
					episodeCode = fmt.Sprintf("S%02dE%02d", it.ParentIndex, it.IndexNumber)
				}

				title := strings.TrimSpace(base)
				if episodeCode != "" {
					title = strings.TrimSpace(episodeCodeRegexp.ReplaceAllString(title, ""))
					title = strings.TrimSpace(strings.Trim(title, "-_:[]() "))
				}

				series := strings.TrimSpace(it.SeriesName)
				if series == "" && episodeCode != "" {
					if idx := strings.Index(title, " - "); idx > 0 {
						series = strings.TrimSpace(title[:idx])
						title = strings.TrimSpace(title[idx+3:])
					}
				}

				core := title
				if series != "" {
					if title == "" || strings.EqualFold(series, title) {
						core = series
					} else {
						core = series + " " + title
					}
				}
				if core == "" {
					core = base
				}

				if episodeCode != "" {
					core = fmt.Sprintf("%s - [%s]", core, episodeCode)
				}
				if ext == "" {
					displayName = fmt.Sprintf("%s (ID%s)", core, id)
				} else {
					displayName = fmt.Sprintf("%s (ID%s)%s", core, id, ext)
				}
			}
		}

		obj := &model.Object{
			ID:       it.ID,
			Name:     displayName,
			Path:     path.Join(parentPath, displayName),
			Size:     it.Size,
			Modified: modified,
			IsFolder: it.IsFolder,
		}
		if it.IsFolder {
			obj.Size = 0
		}
		objs = append(objs, obj)
	}
	return objs, nil
}

func (d *Emby) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if file.IsDir() {
		return nil, errs.NotFile
	}
	if strings.TrimSpace(file.GetID()) == "" {
		return nil, fmt.Errorf("invalid file id")
	}

	u, err := url.Parse(d.URL)
	if err != nil {
		return nil, err
	}
	linkMethod := strings.ToLower(strings.TrimSpace(d.LinkMethod))
	useDownload := linkMethod == "download"

	mediaSourceID := ""
	mediaContainer := ""
	if !useDownload {
		detailURL, parseErr := url.Parse(d.URL + "/Users/" + d.userID + "/Items/" + file.GetID())
		if parseErr == nil {
			q := detailURL.Query()
			q.Set("Fields", "MediaSources")
			q.Set("api_key", d.token)
			detailURL.RawQuery = q.Encode()

			req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, detailURL.String(), nil)
			if reqErr == nil {
				resp, doErr := d.client.Do(req)
				if doErr == nil {
					func() {
						defer resp.Body.Close()
						if resp.StatusCode < 200 || resp.StatusCode >= 300 {
							return
						}
						var detail itemDetailResp
						if decodeErr := json.NewDecoder(resp.Body).Decode(&detail); decodeErr != nil || len(detail.MediaSources) == 0 {
							return
						}
						for i := range detail.MediaSources {
							if strings.TrimSpace(detail.MediaSources[i].ID) != "" && detail.MediaSources[i].SupportsDirectStream {
								mediaSourceID = strings.TrimSpace(detail.MediaSources[i].ID)
								mediaContainer = strings.TrimSpace(detail.MediaSources[i].Container)
								return
							}
						}
						for i := range detail.MediaSources {
							if strings.TrimSpace(detail.MediaSources[i].ID) != "" {
								mediaSourceID = strings.TrimSpace(detail.MediaSources[i].ID)
								mediaContainer = strings.TrimSpace(detail.MediaSources[i].Container)
								return
							}
						}
					}()
				}
			}
		}
	}

	if useDownload {
		u.Path = path.Join(u.Path, "/Items", file.GetID(), "Download")
	} else {
		if mediaContainer != "" {
			u.Path = path.Join(u.Path, "/Videos", file.GetID(), "stream."+mediaContainer)
		} else {
			u.Path = path.Join(u.Path, "/Videos", file.GetID(), "stream")
		}
	}
	q := u.Query()
	q.Set("api_key", d.token)
	if mediaSourceID != "" {
		q.Set("MediaSourceId", mediaSourceID)
	}
	if !useDownload {
		q.Set("Static", "true")
	}
	u.RawQuery = q.Encode()

	return &model.Link{
		URL: u.String(),
		Header: http.Header{
			"User-Agent": []string{base.UserAgent},
		},
	}, nil
}

var _ driver.Driver = (*Emby)(nil)
