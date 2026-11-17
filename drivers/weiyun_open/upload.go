package weiyun_open

import (
	"context"
	"fmt"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func (d *WeiYunOpen) Put(
	ctx context.Context,
	dstDir model.Obj,
	stream model.FileStreamer,
	up driver.UpdateProgress,
) (model.Obj, error) {
	folder, ok := dstDir.(*Folder)
	if !ok {
		return nil, errs.NotSupport
	}
	cacheUp := model.UpdateProgressWithRange(up, 0, cacheProgressEnd)
	file, err := stream.CacheFullAndWriter(&cacheUp, nil)
	if err != nil {
		return nil, err
	}
	req, err := buildUploadRequest(file, stream.GetName(), stream.GetSize(), folder.DirKey)
	if err != nil {
		return nil, err
	}
	uploadUp := model.UpdateProgressWithRange(up, cacheProgressEnd, 100)
	resp, err := d.uploadFile(ctx, file, req, uploadUp)
	if err != nil {
		return nil, err
	}
	return d.finalizeUpload(ctx, folder, resp, stream.GetName(), stream.GetSize())
}

func (d *WeiYunOpen) uploadFile(
	ctx context.Context,
	file model.File,
	req *preUploadArgs,
	up driver.UpdateProgress,
) (*uploadResponse, error) {
	for round := 0; round < maxUploadRounds; round++ {
		resp, err := d.preUpload(ctx, req)
		if err != nil {
			return nil, err
		}
		if resp.FileExist || resp.UploadState == uploadStateDone {
			up(100)
			return resp, nil
		}
		channel, ok := pendingChannel(resp.ChannelList)
		if !ok {
			return nil, fmt.Errorf("weiyun upload returned no pending channel, state=%d", resp.UploadState)
		}
		chunk, err := readChunk(file, int64(channel.Offset), int64(channel.Len))
		if err != nil {
			return nil, err
		}
		resp, err = d.uploadChunk(ctx, req, resp, channel, chunk)
		if err != nil {
			return nil, err
		}
		if resp.UploadState == uploadStateDone {
			up(100)
			return resp, nil
		}
		up(uploadProgress(channel, len(chunk), req.FileSize))
	}
	return nil, fmt.Errorf("weiyun upload exceeded %d rounds", maxUploadRounds)
}

func (d *WeiYunOpen) preUpload(ctx context.Context, req *preUploadArgs) (*uploadResponse, error) {
	resp := uploadResponse{}
	err := d.client.call(ctx, "weiyun.upload", req, &resp)
	if err != nil {
		return nil, err
	}
	if err = responseError(resp.toolResponse); err != nil {
		return nil, err
	}
	if resp.FileName == "" {
		resp.FileName = req.FileName
	}
	return &resp, nil
}

func (d *WeiYunOpen) uploadChunk(
	ctx context.Context,
	req *preUploadArgs,
	state *uploadResponse,
	channel uploadChannel,
	chunk []byte,
) (*uploadResponse, error) {
	resp := uploadResponse{}
	args := uploadChunkArgs{
		FileName:     req.FileName,
		FileSize:     req.FileSize,
		FileSHA:      req.FileSHA,
		BlockSHAList: []string{},
		CheckSHA:     req.CheckSHA,
		UploadKey:    state.UploadKey,
		ChannelList:  state.ChannelList,
		ChannelID:    uint32(channel.ID),
		Ex:           state.Ex,
		FileData:     chunk,
	}
	err := d.client.call(ctx, "weiyun.upload", args, &resp)
	if err != nil {
		return nil, err
	}
	if err = responseError(resp.toolResponse); err != nil {
		return nil, err
	}
	if resp.FileName == "" {
		resp.FileName = req.FileName
	}
	return &resp, nil
}

func pendingChannel(channels []uploadChannel) (uploadChannel, bool) {
	for _, channel := range channels {
		if channel.Len > 0 {
			return channel, true
		}
	}
	return uploadChannel{}, false
}

func uploadProgress(channel uploadChannel, chunkLen int, total uint64) float64 {
	done := uint64(channel.Offset) + uint64(chunkLen)
	if done > total {
		done = total
	}
	return float64(done) * 100 / float64(total)
}

var _ driver.PutResult = (*WeiYunOpen)(nil)
