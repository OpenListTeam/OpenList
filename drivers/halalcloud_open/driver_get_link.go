package halalcloudopen

import (
	"context"
	"crypto/sha1"
	"io"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	sdkUserFile "github.com/halalcloud/golang-sdk-lite/halalcloud/services/userfile"
	"github.com/rclone/rclone/lib/readers"
)

func (d *HalalCloudOpen) getLink(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if args.Redirect {
		// return nil, model.ErrUnsupported
		fid := file.GetID()
		fpath := file.GetPath()
		if fid != "" {
			fpath = ""
		}
		fi, err := d.sdkUserFileService.GetDirectDownloadAddress(ctx, &sdkUserFile.DirectDownloadRequest{
			Identity: fid,
			Path:     fpath,
		})
		if err != nil {
			return nil, err
		}
		expireAt := fi.ExpireAt
		duration := time.Until(time.UnixMilli(expireAt))
		return &model.Link{
			URL:        fi.DownloadAddress,
			Expiration: &duration,
		}, nil
	}
	result, err := d.sdkUserFileService.ParseFileSlice(ctx, &sdkUserFile.File{
		Identity: file.GetID(),
		Path:     file.GetPath(),
	})
	if err != nil {
		return nil, err
	}
	fileAddrs := []*sdkUserFile.SliceDownloadInfo{}
	var addressDuration int64

	nodesNumber := len(result.RawNodes)
	nodesIndex := nodesNumber - 1
	startIndex, endIndex := 0, nodesIndex
	for nodesIndex >= 0 {
		if nodesIndex >= 200 {
			endIndex = 200
		} else {
			endIndex = nodesNumber
		}
		for ; endIndex <= nodesNumber; endIndex += 200 {
			if endIndex == 0 {
				endIndex = 1
			}
			sliceAddress, err := d.sdkUserFileService.GetSliceDownloadAddress(ctx, &sdkUserFile.SliceDownloadAddressRequest{
				Identity: result.RawNodes[startIndex:endIndex],
				Version:  1,
			})
			if err != nil {
				return nil, err
			}
			addressDuration, _ = strconv.ParseInt(sliceAddress.ExpireAt, 10, 64)
			fileAddrs = append(fileAddrs, sliceAddress.Addresses...)
			startIndex = endIndex
			nodesIndex -= 200
		}

	}

	size, _ := strconv.ParseInt(result.FileSize, 10, 64)
	chunks := getChunkSizes(result.Sizes)
	resultRangeReader := func(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
		length := httpRange.Length
		if httpRange.Length < 0 || httpRange.Start+httpRange.Length >= size {
			length = size - httpRange.Start
		}
		oo := &openObject{
			ctx:     ctx,
			d:       fileAddrs,
			chunk:   []byte{},
			chunks:  chunks,
			skip:    httpRange.Start,
			sha:     result.Sha1,
			shaTemp: sha1.New(),
		}

		return readers.NewLimitedReadCloser(oo, length), nil
	}

	var duration time.Duration
	if addressDuration != 0 {
		duration = time.Until(time.UnixMilli(addressDuration))
	} else {
		duration = time.Until(time.Now().Add(time.Hour))
	}

	return &model.Link{
		RangeReader: stream.RateLimitRangeReaderFunc(resultRangeReader),
		Expiration:  &duration,
	}, nil
}
