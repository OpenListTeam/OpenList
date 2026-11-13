package foxel

import "encoding/json"

type response struct {
	Code   *int            `json:"code"`
	Msg    string          `json:"msg"`
	Data   json.RawMessage `json:"data"`
	Detail json.RawMessage `json:"detail"`
}

type entry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

type listing struct {
	Entries    []entry `json:"entries"`
	Pagination struct {
		Mode       string `json:"mode"`
		Pages      *int   `json:"pages"`
		Total      *int   `json:"total"`
		PageSize   int    `json:"page_size"`
		NextCursor string `json:"next_cursor"`
		HasNext    bool   `json:"has_next"`
	} `json:"pagination"`
}

type tempLink struct {
	Token string `json:"token"`
	URL   string `json:"url"`
}

type transferResult struct {
	Moved   bool   `json:"moved"`
	Copied  bool   `json:"copied"`
	Renamed bool   `json:"renamed"`
	Queued  bool   `json:"queued"`
	TaskID  string `json:"task_id"`
}

type taskStatus struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

type uploadResult struct {
	Uploaded bool   `json:"uploaded"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
}
