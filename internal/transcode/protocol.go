package transcode

// 协议版本
const ProtocolVersion = "v1"

// ---- 注册 ----

type RegisterRequest struct {
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Capacity      int      `json:"capacity"`
	HWAccel       []string `json:"hwaccel"`
	CodecsDecode  []string `json:"codecs_decode"`
	CodecsEncode  []string `json:"codecs_encode"`
	MaxResolution string   `json:"max_resolution"`
	Tags          []string `json:"tags"`
	Endpoint      string   `json:"endpoint,omitempty"`
}

type RegisterResponse struct {
	WorkerID          string `json:"worker_id"`
	HeartbeatInterval int    `json:"heartbeat_interval"`
	ClaimStrategy     string `json:"claim_strategy"` // pull
	ProtocolVersion   string `json:"protocol_version"`
}

// ---- 心跳 ----

type HeartbeatRequest struct {
	WorkerID  string   `json:"worker_id"`
	Load      float64  `json:"load"`
	Running   []string `json:"running"`
	FreeSlots int      `json:"free_slots"`
}

type HeartbeatResponse struct {
	OK     bool     `json:"ok"`
	Kick   bool     `json:"kick"`             // 服务端要求 worker 退出
	Cancel []string `json:"cancel,omitempty"` // 需要取消的 job ids
}

// ---- 领任务 ----

type ClaimRequest struct {
	WorkerID string `json:"worker_id"`
	Slots    int    `json:"slots"`
	Wait     int    `json:"wait"` // 长轮询超时秒，0=不等
}

type ClaimResponse struct {
	Jobs []*Job `json:"jobs"`
}

// ---- 完成 ----

type FinishRequest struct {
	JobID  string    `json:"job_id"`
	Status JobStatus `json:"status"`
	Error  string    `json:"error,omitempty"`
	Stats  JobStats  `json:"stats"`
}

type JobStats struct {
	Elapsed   float64 `json:"elapsed"`
	FPS       float64 `json:"fps"`
	Speed     float64 `json:"speed"`
	BytesOut  int64   `json:"bytes_out"`
}

// ---- 通用响应 ----

type ErrorResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
