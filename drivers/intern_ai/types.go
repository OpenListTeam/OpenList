package intern_ai

import "encoding/json"

// ----- Generic API envelope -----

// flexString tolerates fields that some deployments return as JSON strings and
// others as JSON numbers (e.g. modified_at is a unix-seconds number in
// production but an RFC3339 string elsewhere).
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
        b = bytesTrimSpace(b)
        if len(b) == 0 || string(b) == "null" {
                *s = ""
                return nil
        }
        if b[0] == '"' {
                var v string
                if err := json.Unmarshal(b, &v); err != nil {
                        return err
                }
                *s = flexString(v)
                return nil
        }
        // number / bool / other literal: keep the raw JSON text
        *s = flexString(b)
        return nil
}

func bytesTrimSpace(b []byte) []byte {
        for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\n' || b[0] == '\r') {
                b = b[1:]
        }
        for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t' || b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
                b = b[:len(b)-1]
        }
        return b
}

// apiResp is the common response envelope of the InternAI platform.
//
// NOTE: most endpoints answer {code,msg,data}, but upload-url is known to
// answer the payload directly at the top level ({"urls":[...]}). TopLevelURLs
// catches that shape and request() normalizes it into Data.URLs.
type apiResp struct {
        Code         int        `json:"code"`
        Msg          string     `json:"msg"`
        Success      bool       `json:"success"`
        TopLevelURLs []urlItem  `json:"urls"`
        Data         struct {
                Files       []fileItem `json:"files"`
                NextToken   string     `json:"next_page_token"`
                Total       int64      `json:"total"`
                HasMore     bool       `json:"has_more"`
                URLs        []urlItem  `json:"urls"`
                Results     []checkRes `json:"results"`
                Names       []string   `json:"names"`
                UsedGb      float64    `json:"used_gb"`
                TotalGb     float64    `json:"total_gb"`
                UsedBytes   int64      `json:"used_bytes"`
                TotalBytes  int64      `json:"total_bytes"`
                CloudUsedGb float64    `json:"cloud_used_gb"`
                CloudTotal  float64    `json:"cloud_total_gb"`
        } `json:"data"`
}

// fileItem is one entry of the cloud-disk/list response.
type fileItem struct {
        // FileKey is the full path of the entry, e.g. "/docs/readme.txt"
        FileKey     string `json:"file_key"`
        FileName    string `json:"file_name"`
        IsDir       bool   `json:"is_dir"`
        Size        int64  `json:"size"`
        ContentType string `json:"content_type"`
        // ModifiedAt is a unix-seconds number in production but may be an
        // RFC3339 string on other deployments.
        ModifiedAt flexString `json:"modified_at"`
        // CreatedAt is returned by some deployments
        CreatedAt flexString `json:"created_at"`
        // CloudFileStatus is a UI hint ("1" creating, "2" renaming) and ignored here
        CloudFileStatus flexString `json:"cloud_file_status"`
}

// urlItem is one entry of download-url / upload-url responses.
type urlItem struct {
        FileKey     string `json:"file_key"`
        FileName    string `json:"file_name"`
        DownloadURL string `json:"download_url"`
        UploadURL   string `json:"upload_url"`
        Skipped     bool   `json:"skipped"`
}

// checkRes is one entry of the check (name conflict) response.
type checkRes struct {
        FileKey string `json:"file_key"`
        Exists  bool   `json:"exists"`
}
