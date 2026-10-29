package stream

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"testing/synctest"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/stretchr/testify/require"
)

type resumeTestReader func([]byte) (int, error)

func (f resumeTestReader) Read(p []byte) (int, error) { return f(p) }

func TestResumableReaderResumes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const data = "openlist"
		chunk := func(offset int64) io.ReadCloser {
			return io.NopCloser(iotest.DataErrReader(strings.NewReader(data[offset : offset+1])))
		}
		var offsets []int64
		rr := RangeReaderFunc(func(ctx context.Context, r http_range.Range) (io.ReadCloser, error) {
			offsets = append(offsets, r.Start)
			return chunk(r.Start), nil
		})
		r := &SeekableStream{
			FileStream: &FileStream{
				Ctx: context.Background(),
				Obj: &model.Object{Size: int64(len(data))},
			},
			rangeReader: rr,
		}
		defer r.Close()

		got, err := io.ReadAll(r)
		require.NoError(t, err)
		require.Equal(t, data, string(got))
		require.Equal(t, []int64{0, 1, 2, 3, 4, 5, 6, 7}, offsets)
	})
}

func TestResumableReaderRetryLimit(t *testing.T) {
	requestErr := errors.New("range request failed")
	for _, failure := range []error{io.EOF, io.ErrUnexpectedEOF, requestErr} {
		t.Run(failure.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				broken := func() io.ReadCloser {
					return io.NopCloser(resumeTestReader(func([]byte) (int, error) { return 0, failure }))
				}
				attempts := 0
				rr := RangeReaderFunc(func(ctx context.Context, r http_range.Range) (io.ReadCloser, error) {
					attempts++
					if failure == requestErr {
						return nil, failure
					}
					return broken(), nil
				})
				r := &resumableReader{ctx: context.Background(), rr: rr, rc: broken()}
				defer r.Close()

				_, err := io.ReadAll(r)
				want := failure
				if want == io.EOF {
					want = io.ErrUnexpectedEOF
				}
				require.ErrorIs(t, err, want)
				require.Equal(t, maxResumeAttempts, attempts)
			})
		})
	}
}

func TestSeekableStreamSizeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int64
		err  error
	}{
		{name: "known", size: 8},
		{name: "unknown", size: -1, err: io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			r := &SeekableStream{
				FileStream: &FileStream{Ctx: context.Background(), Obj: &model.Object{Size: tc.size}},
				rangeReader: RangeReaderFunc(func(context.Context, http_range.Range) (io.ReadCloser, error) {
					return io.NopCloser(resumeTestReader(func(p []byte) (int, error) {
						reads++
						require.Equal(t, 1, reads, "must not read upstream again")
						return copy(p, "openlist"), io.ErrUnexpectedEOF
					})), nil
				}),
			}
			defer r.Close()

			got, err := io.ReadAll(r)
			require.ErrorIs(t, err, tc.err)
			require.Equal(t, "openlist", string(got))
		})
	}
}
