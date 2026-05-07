package weiyun_open

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	streamPkg "github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/errgroup"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/avast/retry-go"
)

type uploadChannelsOptions struct {
	sections   streamPkg.StreamSectionReaderIF
	req        *preUploadArgs
	state      *uploadResponse
	channels   []uploadChannel
	nextOffset *int64
	up         driver.UpdateProgress
}

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
	req, err := buildUploadRequest(stream, stream.GetName(), stream.GetSize(), folder.DirKey)
	if err != nil {
		return nil, err
	}
	cacheUp(100)
	uploadUp := model.UpdateProgressWithRange(up, cacheProgressEnd, 100)
	sections, err := streamPkg.NewStreamSectionReader(stream, int(uploadBlockSize), &uploadUp)
	if err != nil {
		return nil, err
	}
	resp, err := d.uploadFile(ctx, sections, req, uploadUp)
	if err != nil {
		return nil, err
	}
	return d.finalizeUpload(ctx, folder, resp, stream.GetName(), stream.GetSize())
}

func (d *WeiYunOpen) uploadFile(
	ctx context.Context,
	sections streamPkg.StreamSectionReaderIF,
	req *preUploadArgs,
	up driver.UpdateProgress,
) (*uploadResponse, error) {
	nextOffset := int64(0)
	for round := 0; round < maxUploadRounds; round++ {
		resp, err := d.preUpload(ctx, req)
		if err != nil {
			return nil, err
		}
		if resp.FileExist || resp.UploadState == uploadStateDone {
			up(100)
			return resp, nil
		}
		channels := pendingChannels(resp.ChannelList)
		if len(channels) == 0 {
			continue
		}
		err = d.uploadChannels(ctx, uploadChannelsOptions{
			sections:   sections,
			req:        req,
			state:      resp,
			channels:   channels,
			nextOffset: &nextOffset,
			up:         up,
		})
		if err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("weiyun upload exceeded %d rounds", maxUploadRounds)
}

func (d *WeiYunOpen) uploadChannels(ctx context.Context, options uploadChannelsOptions) error {
	limit := min(len(options.channels), d.UploadThread)
	group, uploadCtx := errgroup.NewOrderedGroupWithContext(ctx, limit,
		retry.Attempts(3),
		retry.Delay(time.Second),
		retry.DelayType(retry.BackOffDelay))
	for _, channel := range options.channels {
		if utils.IsCanceled(uploadCtx) {
			break
		}
		group.GoWithLifecycle(d.uploadChannelLifecycle(options, channel))
	}
	return group.Wait()
}

func (d *WeiYunOpen) uploadChannelLifecycle(
	options uploadChannelsOptions,
	channel uploadChannel,
) errgroup.Lifecycle {
	var reader io.ReadSeeker
	return errgroup.Lifecycle{
		Before: func(ctx context.Context) (err error) {
			reader, err = nextChannelReader(options, channel)
			return err
		},
		Do: func(ctx context.Context) error {
			chunk, err := readUploadChunk(reader, int(channel.Len))
			if err != nil {
				return err
			}
			if _, err = d.uploadChunk(ctx, options.req, options.state, channel, chunk); err != nil {
				return err
			}
			options.up(uploadProgress(channel, len(chunk), options.req.FileSize))
			return nil
		},
		After: func(err error) {
			options.sections.FreeSectionReader(reader)
		},
	}
}

func nextChannelReader(options uploadChannelsOptions, channel uploadChannel) (io.ReadSeeker, error) {
	offset := int64(channel.Offset)
	if offset > *options.nextOffset {
		if err := options.sections.DiscardSection(*options.nextOffset, offset-*options.nextOffset); err != nil {
			return nil, err
		}
	}
	reader, err := options.sections.GetSectionReader(offset, int64(channel.Len))
	*options.nextOffset = offset + int64(channel.Len)
	return reader, err
}

func readUploadChunk(reader io.ReadSeeker, expected int) ([]byte, error) {
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	chunk, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if len(chunk) != expected {
		return nil, fmt.Errorf("expected %d bytes, got %d", expected, len(chunk))
	}
	return chunk, nil
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

func pendingChannels(channels []uploadChannel) []uploadChannel {
	pending := make([]uploadChannel, 0, len(channels))
	for _, channel := range channels {
		if channel.Len > 0 {
			pending = append(pending, channel)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].Offset < pending[j].Offset
	})
	return pending
}

func uploadProgress(channel uploadChannel, chunkLen int, total uint64) float64 {
	done := uint64(channel.Offset) + uint64(chunkLen)
	if done > total {
		done = total
	}
	return float64(done) * 100 / float64(total)
}

var _ driver.PutResult = (*WeiYunOpen)(nil)
