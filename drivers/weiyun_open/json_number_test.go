package weiyun_open

import (
	"encoding/json"
	"testing"
)

func TestListResponseUnmarshalStringNumbers(t *testing.T) {
	data := []byte(`{
		"pdir_key": "parent",
		"total_dir_count": "1",
		"total_file_count": 1,
		"dir_list": [{
			"dir_key": "dir-1",
			"dir_name": "docs",
			"dir_ctime": "1712312312000",
			"dir_mtime": 1712312313000
		}],
		"file_list": [{
			"file_id": "file-1",
			"filename": "readme.txt",
			"file_size": "12",
			"file_ctime": "1712312314000",
			"file_mtime": 1712312315000,
			"pdir_key": "ignored"
		}],
		"finish_flag": true
	}`)

	resp := listResponse{}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if got := int64(resp.DirList[0].DirCTime); got != 1712312312000 {
		t.Fatalf("unexpected dir_ctime: %d", got)
	}
	if got := int64(resp.FileList[0].FileSize); got != 12 {
		t.Fatalf("unexpected file_size: %d", got)
	}
	if got := uint32(resp.TotalDirCount); got != 1 {
		t.Fatalf("unexpected total_dir_count: %d", got)
	}
}

func TestDeleteResponseUnmarshalStringNumbers(t *testing.T) {
	data := []byte(`{
		"freed_space": "13",
		"freed_index_cnt": "1",
		"error": ""
	}`)

	resp := deleteResponse{}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal delete response: %v", err)
	}
	if got := int64(resp.FreedSpace); got != 13 {
		t.Fatalf("unexpected freed_space: %d", got)
	}
	if got := uint32(resp.FreedIndexCnt); got != 1 {
		t.Fatalf("unexpected freed_index_cnt: %d", got)
	}
}
