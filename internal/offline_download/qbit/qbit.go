package qbit

import (
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/qbittorrent"
	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
	"github.com/pkg/errors"
)

const maxQbittorrentTorrentSize = 10 * 1024 * 1024

type QBittorrent struct {
	client qbittorrent.Client
}

func (a *QBittorrent) Run(task *tool.DownloadTask) error {
	return errs.NotSupport
}

func (a *QBittorrent) Name() string {
	return "qBittorrent"
}

func (a *QBittorrent) Items() []model.SettingItem {
	// qBittorrent settings
	return []model.SettingItem{
		{Key: conf.QbittorrentUrl, Value: "http://admin:adminadmin@localhost:8080/", Type: conf.TypeString, Group: model.OFFLINE_DOWNLOAD, Flag: model.PRIVATE},
		{Key: conf.QbittorrentSeedtime, Value: "0", Type: conf.TypeNumber, Group: model.OFFLINE_DOWNLOAD, Flag: model.PRIVATE},
	}
}

func (a *QBittorrent) Init() (string, error) {
	a.client = nil
	url := setting.GetStr(conf.QbittorrentUrl)
	qbClient, err := qbittorrent.New(url)
	if err != nil {
		return "", err
	}
	a.client = qbClient
	return "ok", nil
}

func (a *QBittorrent) IsReady() bool {
	return a.client != nil
}

func (a *QBittorrent) AddURL(args *tool.AddUrlArgs) (string, error) {
	var err error
	if len(args.TorrentData) > 0 {
		err = a.client.AddFromTorrent(args.TorrentData, args.TempDir, args.UID)
	} else if torrentData, ok := fetchTorrentDataFromURL(args); ok {
		err = a.client.AddFromTorrent(torrentData, args.TempDir, args.UID)
	} else {
		err = a.client.AddFromLink(args.Url, args.TempDir, args.UID)
	}
	if err != nil {
		return "", err
	}
	return args.UID, nil
}

func fetchTorrentDataFromURL(args *tool.AddUrlArgs) ([]byte, bool) {
	u, err := url.Parse(strings.TrimSpace(args.Url))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, false
	}

	resp, err := net.RequestHttp(
		args.Ctx,
		http.MethodGet,
		http.Header{"User-Agent": []string{base.UserAgent}},
		args.Url,
	)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxQbittorrentTorrentSize+1))
	if err != nil || len(data) > maxQbittorrentTorrentSize {
		return nil, false
	}
	if _, err := torrent.Decode(data); err != nil {
		return nil, false
	}
	return data, true
}

func (a *QBittorrent) Remove(task *tool.DownloadTask) error {
	err := a.client.Delete(task.GID, false)
	return err
}

func (a *QBittorrent) Status(task *tool.DownloadTask) (*tool.Status, error) {
	info, err := a.client.GetInfo(task.GID)
	if err != nil {
		return nil, err
	}
	s := &tool.Status{}
	s.TotalBytes = info.Size
	if info.Size > 0 {
		s.Progress = float64(info.Completed) / float64(info.Size) * 100
	} else {
		s.Progress = info.Progress * 100
	}
	switch info.State {
	case qbittorrent.UPLOADING, qbittorrent.PAUSEDUP, qbittorrent.QUEUEDUP, qbittorrent.STALLEDUP, qbittorrent.FORCEDUP, qbittorrent.CHECKINGUP:
		s.Completed = true
	case qbittorrent.ALLOCATING, qbittorrent.DOWNLOADING, qbittorrent.METADL, qbittorrent.PAUSEDDL, qbittorrent.QUEUEDDL, qbittorrent.STALLEDDL, qbittorrent.CHECKINGDL, qbittorrent.FORCEDDL, qbittorrent.CHECKINGRESUMEDATA, qbittorrent.MOVING:
		s.Status = "[qBittorrent] downloading"
	case qbittorrent.ERROR, qbittorrent.MISSINGFILES, qbittorrent.UNKNOWN:
		s.Err = errors.Errorf("[qBittorrent] failed to download %s, error: %s", task.GID, info.State)
	default:
		s.Err = errors.Errorf("[qBittorrent] unknown error occurred downloading %s", task.GID)
	}
	return s, nil
}

var _ tool.Tool = (*QBittorrent)(nil)

func init() {
	tool.Tools.Add(&QBittorrent{})
}
