package model

import "time"

// Job statuses
const (
	StatusPending  = "pending"  // just submitted, waiting for metadata fetch
	StatusReady    = "ready"    // metadata fetched, waiting for user format selection
	StatusQueued   = "queued"   // user selected format, waiting for download
	StatusRunning  = "running"  // worker is downloading
	StatusDone     = "done"     // download complete, file available
	StatusFailed   = "failed"   // permanent failure
	StatusCanceled = "canceled" // user or system canceled
)

// Download modes
const (
	ModeVideoAudio = "video+audio"
	ModeVideoOnly  = "video"
	ModeAudioOnly  = "audio"
	ModeBest       = "best" // let yt-dlp pick best format automatically
)

type Job struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Mode      string    `json:"mode,omitempty"`
	VideoItag string    `json:"video_itag,omitempty"`
	AudioItag string    `json:"audio_itag,omitempty"`
	FilePath  string    `json:"file_path,omitempty"`
	FileName  string    `json:"file_name,omitempty"`
	Error     string    `json:"error,omitempty"`
	WorkerID     string    `json:"worker_id,omitempty"`
	ProxyNote    string    `json:"proxy_note,omitempty"`
	TrimStart    string    `json:"trim_start,omitempty"`    // start timestamp, empty = file start
	TrimEnd      string    `json:"trim_end,omitempty"`      // end timestamp, empty = file end
	TrimLastSecs int       `json:"trim_last_secs,omitempty"` // keep last N seconds (overrides Start/End)
	Retries      int       `json:"retries"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Format struct {
	Itag      string `json:"itag"`
	Quality   string `json:"quality"`
	MimeType  string `json:"mime_type"`
	Container string `json:"container"` // mp4, webm
	Type      string `json:"type"`      // video, audio, muxed
	Bitrate   int    `json:"bitrate"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	FPS       int    `json:"fps,omitempty"`
	Label     string `json:"label"` // human-readable
}

type JobMetadata struct {
	JobID   string   `json:"job_id"`
	Title   string   `json:"title"`
	Formats []Format `json:"formats"`
}

type WorkerInfo struct {
	ID         string    `json:"id"`
	Addr       string    `json:"addr,omitempty"`
	MaxJobs    int       `json:"max_jobs"`
	ActiveJobs int       `json:"active_jobs"`
	CoolUntil  time.Time `json:"cool_until"`
	LastSeen   time.Time `json:"last_seen"`
}

type WorkerRegisterReq struct {
	ID      string `json:"id"`
	Addr    string `json:"addr,omitempty"`
	MaxJobs int    `json:"max_jobs"`
}

type PollResp struct {
	Job *Job `json:"job,omitempty"`
}

type JobUpdateReq struct {
	JobID    string `json:"job_id"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
	WorkerID string `json:"worker_id"`
}

type FormatSelectReq struct {
	Mode      string `json:"mode"`
	VideoItag int    `json:"video_itag,omitempty"`
	AudioItag int    `json:"audio_itag,omitempty"`
}

type StatusResp struct {
	Job     *Job     `json:"job"`
	Formats []Format `json:"formats,omitempty"`
}
