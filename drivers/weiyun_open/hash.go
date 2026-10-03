package weiyun_open

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type sha1State struct {
	h            [5]uint32
	buffer       []byte
	messageBytes uint64
}

func newSHA1State() *sha1State {
	return &sha1State{
		h: [5]uint32{
			0x67452301,
			0xEFCDAB89,
			0x98BADCFE,
			0x10325476,
			0xC3D2E1F0,
		},
		buffer: make([]byte, 0, 64),
	}
}

func (s *sha1State) Update(data []byte) {
	s.messageBytes += uint64(len(data))
	s.buffer = append(s.buffer, data...)
	for len(s.buffer) >= 64 {
		s.processChunk(s.buffer[:64])
		s.buffer = s.buffer[64:]
	}
}

func (s *sha1State) processChunk(chunk []byte) {
	var words [80]uint32
	for i := 0; i < 16; i++ {
		words[i] = binary.BigEndian.Uint32(chunk[i*4 : (i+1)*4])
	}
	for i := 16; i < len(words); i++ {
		words[i] = rol(words[i-3]^words[i-8]^words[i-14]^words[i-16], 1)
	}
	a, b, c, d, e := s.h[0], s.h[1], s.h[2], s.h[3], s.h[4]
	for i := 0; i < len(words); i++ {
		f, k := sha1Round(b, c, d, i)
		temp := rol(a, 5) + f + e + k + words[i]
		e, d, c, b, a = d, c, rol(b, 30), a, temp
	}
	s.h[0] += a
	s.h[1] += b
	s.h[2] += c
	s.h[3] += d
	s.h[4] += e
}

func (s *sha1State) StateHex() (string, error) {
	if len(s.buffer) != 0 {
		return "", fmt.Errorf("sha1 state is not block-aligned: %d", len(s.buffer))
	}
	out := make([]byte, 0, 20)
	for _, value := range s.h {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], value)
		out = append(out, buf[:]...)
	}
	return hex.EncodeToString(out), nil
}

func (s *sha1State) SumHex() string {
	clone := *s
	clone.buffer = append([]byte(nil), s.buffer...)
	padding := sha1Padding(clone.messageBytes, len(clone.buffer))
	clone.Update(padding)
	out := make([]byte, 20)
	for i, value := range clone.h {
		binary.BigEndian.PutUint32(out[i*4:(i+1)*4], value)
	}
	return hex.EncodeToString(out)
}

func buildUploadRequest(file model.File, fileName string, fileSize int64, pdirKey string) (*preUploadArgs, error) {
	if fileSize < 0 {
		return nil, fmt.Errorf("weiyun open upload does not support unknown file size")
	}
	if fileSize == 0 {
		return emptyUploadRequest(fileName, pdirKey), nil
	}
	lastBlockSize := blockTailSize(fileSize)
	checkBlockSize := blockCheckSize(lastBlockSize)
	beforeBlockSize := fileSize - lastBlockSize
	md5Hash := md5.New()
	sha1Hash := newSHA1State()
	blockSHAList, err := collectBlockHashes(file, beforeBlockSize, sha1Hash, md5Hash)
	if err != nil {
		return nil, err
	}
	checkSHA, checkData, fileSHA, err := finishLastBlock(
		file, beforeBlockSize, lastBlockSize, checkBlockSize, sha1Hash, md5Hash,
	)
	if err != nil {
		return nil, err
	}
	blockSHAList = append(blockSHAList, fileSHA)
	return &preUploadArgs{
		FileName:     fileName,
		FileSize:     uint64(fileSize),
		FileSHA:      fileSHA,
		FileMD5:      hex.EncodeToString(md5Hash.Sum(nil)),
		BlockSHAList: blockSHAList,
		CheckSHA:     checkSHA,
		CheckData:    checkData,
		PdirKey:      pdirKey,
	}, nil
}

func emptyUploadRequest(fileName string, pdirKey string) *preUploadArgs {
	return &preUploadArgs{
		FileName:     fileName,
		FileSize:     0,
		FileSHA:      emptyFileSHA1,
		FileMD5:      emptyFileMD5,
		BlockSHAList: []string{emptyFileSHA1},
		CheckSHA:     emptySHA1StateHex,
		CheckData:    "",
		PdirKey:      pdirKey,
	}
}

func collectBlockHashes(
	file model.File,
	beforeBlockSize int64,
	sha1Hash *sha1State,
	md5Hash io.Writer,
) ([]string, error) {
	blockCount := int(beforeBlockSize/uploadBlockSize) + 1
	blockSHAList := make([]string, 0, blockCount)
	for offset := int64(0); offset < beforeBlockSize; offset += uploadBlockSize {
		chunk, err := readChunk(file, offset, uploadBlockSize)
		if err != nil {
			return nil, err
		}
		sha1Hash.Update(chunk)
		if _, err = md5Hash.Write(chunk); err != nil {
			return nil, err
		}
		state, err := sha1Hash.StateHex()
		if err != nil {
			return nil, err
		}
		blockSHAList = append(blockSHAList, state)
	}
	return blockSHAList, nil
}

func finishLastBlock(
	file model.File,
	beforeBlockSize int64,
	lastBlockSize int64,
	checkBlockSize int64,
	sha1Hash *sha1State,
	md5Hash io.Writer,
) (string, string, string, error) {
	middleSize := lastBlockSize - checkBlockSize
	if middleSize > 0 {
		middle, err := readChunk(file, beforeBlockSize, middleSize)
		if err != nil {
			return "", "", "", err
		}
		sha1Hash.Update(middle)
		if _, err = md5Hash.Write(middle); err != nil {
			return "", "", "", err
		}
	}
	checkSHA, err := sha1Hash.StateHex()
	if err != nil {
		return "", "", "", err
	}
	tail, err := readChunk(file, beforeBlockSize+middleSize, checkBlockSize)
	if err != nil {
		return "", "", "", err
	}
	sha1Hash.Update(tail)
	if _, err = md5Hash.Write(tail); err != nil {
		return "", "", "", err
	}
	return checkSHA, base64.StdEncoding.EncodeToString(tail), sha1Hash.SumHex(), nil
}

func readChunk(file model.File, offset int64, length int64) ([]byte, error) {
	reader := io.NewSectionReader(file, offset, length)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != length {
		return nil, fmt.Errorf("expected %d bytes, got %d", length, len(data))
	}
	return data, nil
}

func blockTailSize(fileSize int64) int64 {
	tail := fileSize % uploadBlockSize
	if tail == 0 {
		return uploadBlockSize
	}
	return tail
}

func blockCheckSize(lastBlockSize int64) int64 {
	size := lastBlockSize % checkBlockDivisor
	if size == 0 {
		return checkBlockDivisor
	}
	return size
}

func sha1Round(b, c, d uint32, index int) (uint32, uint32) {
	switch {
	case index < 20:
		return (b & c) | (^b & d), 0x5A827999
	case index < 40:
		return b ^ c ^ d, 0x6ED9EBA1
	case index < 60:
		return (b & c) | (b & d) | (c & d), 0x8F1BBCDC
	default:
		return b ^ c ^ d, 0xCA62C1D6
	}
}

func sha1Padding(messageBytes uint64, buffered int) []byte {
	padding := []byte{0x80}
	zeros := (56 - (buffered+1)%64 + 64) % 64
	padding = append(padding, make([]byte, zeros)...)
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], messageBytes*8)
	return append(padding, length[:]...)
}

func rol(value uint32, bits uint) uint32 {
	return value<<bits | value>>(32-bits)
}
