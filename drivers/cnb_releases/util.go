package cnb_releases

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	log "github.com/sirupsen/logrus"
)

// do others that not defined in Driver interface

func (d *CnbReleases) Request(method string, path string, callback base.ReqCallback, resp any) error {
	if d.ref != nil {
		return d.ref.Request(method, path, callback, resp)
	}
	var url string
	if strings.HasPrefix(path, "http") {
		url = path
	} else {
		url = "https://api.cnb.cool" + path
	}
	req := base.RestyClient.R()
	req.SetHeader("Accept", "application/json")
	req.SetAuthScheme("Bearer")
	req.SetAuthToken(d.Token)

	if callback != nil {
		callback(req)
	}
	res, err := req.Execute(method, url)
	log.Debugln(res.String())
	if err != nil {
		return err
	}
	if res.StatusCode() != http.StatusOK && res.StatusCode() != http.StatusCreated && res.StatusCode() != http.StatusNoContent {
		return fmt.Errorf("failed to request %s, status code: %d, message: %s", url, res.StatusCode(), res.String())
	}

	if resp != nil {
		err = json.Unmarshal(res.Body(), resp)
		if err != nil {
			return err
		}
	}

	return nil
}

func (d *CnbReleases) sumAssetsSize(assets []ReleaseAsset) int64 {
	var size int64
	for _, asset := range assets {
		size += asset.Size
	}
	return size
}
