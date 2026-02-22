package controller

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"youdl/internal/model"
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.render(w, "index.html", nil)
}

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	// Honeypot: bots fill in hidden fields, humans leave them empty.
	if r.FormValue("website") != "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	ip := clientIP(r)

	// IP rate limit
	if !s.rl.Allow(ip) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}

	// Per-IP active job cap
	if s.cfg.MaxJobsPerIP > 0 {
		count, err := s.db.ActiveJobsForIP(ip)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if count >= s.cfg.MaxJobsPerIP {
			http.Error(w, "too many active jobs for your IP", http.StatusTooManyRequests)
			return
		}
	}

	// Global queue depth cap
	if s.cfg.MaxQueueDepth > 0 {
		total, err := s.db.ActiveJobsTotal()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if total >= s.cfg.MaxQueueDepth {
			http.Error(w, "server queue is full, try again later", http.StatusServiceUnavailable)
			return
		}
	}

	rawURL := strings.TrimSpace(r.FormValue("url"))
	if rawURL == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		http.Error(w, "invalid URL", http.StatusBadRequest)
		return
	}
	if isPrivateHost(parsed.Host) {
		http.Error(w, "URL must not point to a private or internal address", http.StatusBadRequest)
		return
	}
	// YouTube-specific sanitization
	submitURL := rawURL
	if strings.Contains(parsed.Host, "youtube.com") || strings.Contains(parsed.Host, "youtu.be") {
		submitURL = sanitizeYouTubeURL(rawURL)
	}

	id := generateID()
	now := time.Now().UTC()
	job := &model.Job{
		ID:        id,
		URL:       submitURL,
		Status:    model.StatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.db.CreateJob(job, ip); err != nil {
		log.Printf("create job: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	log.Printf("job created: %s for %s (ip=%s)", id, submitURL, ip)
	http.Redirect(w, r, "/job/"+id, http.StatusSeeOther)
}

func (s *Server) handleJobPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, err := s.db.GetJob(id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if job == nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	data := map[string]any{
		"Job": job,
	}

	if job.Status == model.StatusReady {
		formats, _ := s.db.GetFormats(id)
		data["Formats"] = formats
		// Separate video and audio formats
		var videoFormats, audioFormats []model.Format
		for _, f := range formats {
			switch f.Type {
			case "video":
				videoFormats = append(videoFormats, f)
			case "audio":
				audioFormats = append(audioFormats, f)
			}
		}
		data["VideoFormats"] = videoFormats
		data["AudioFormats"] = audioFormats
	}

	s.render(w, "job.html", data)
}

func (s *Server) handleFormatSelect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, err := s.db.GetJob(id)
	if err != nil || job == nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	if job.Status != model.StatusReady {
		http.Error(w, "job not in ready state", http.StatusBadRequest)
		return
	}

	mode := r.FormValue("mode")
	videoItag := strings.TrimSpace(r.FormValue("video_itag"))
	audioItag := strings.TrimSpace(r.FormValue("audio_itag"))

	// Build a lookup of stored itag → format type for validation.
	formats, err := s.db.GetFormats(id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	formatType := make(map[string]string, len(formats))
	for _, f := range formats {
		formatType[f.Itag] = f.Type
	}

	switch mode {
	case model.ModeVideoAudio:
		if videoItag == "" {
			http.Error(w, "video_itag required for video+audio mode", http.StatusBadRequest)
			return
		}
		if formatType[videoItag] != "video" {
			http.Error(w, "invalid video_itag", http.StatusBadRequest)
			return
		}
		// Auto-select best audio if not provided; otherwise validate.
		if audioItag == "" {
			audioItag = s.bestAudioItag(id, videoItag)
		} else if formatType[audioItag] != "audio" {
			http.Error(w, "invalid audio_itag", http.StatusBadRequest)
			return
		}
	case model.ModeVideoOnly:
		if videoItag == "" {
			http.Error(w, "video_itag required", http.StatusBadRequest)
			return
		}
		if t := formatType[videoItag]; t != "video" && t != "muxed" {
			http.Error(w, "invalid video_itag", http.StatusBadRequest)
			return
		}
	case model.ModeAudioOnly:
		if audioItag == "" {
			http.Error(w, "audio_itag required", http.StatusBadRequest)
			return
		}
		if formatType[audioItag] != "audio" {
			http.Error(w, "invalid audio_itag", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "invalid mode", http.StatusBadRequest)
		return
	}

	// Trim options
	switch r.FormValue("trim_mode") {
	case "range":
		start := strings.TrimSpace(r.FormValue("trim_start"))
		end := strings.TrimSpace(r.FormValue("trim_end"))
		if start != "" && !validTimestamp(start) {
			http.Error(w, "invalid trim start timestamp", http.StatusBadRequest)
			return
		}
		if end != "" && !validTimestamp(end) {
			http.Error(w, "invalid trim end timestamp", http.StatusBadRequest)
			return
		}
		job.TrimStart = start
		job.TrimEnd = end
	case "first":
		secs, _ := strconv.Atoi(r.FormValue("trim_secs"))
		if secs > 0 {
			job.TrimEnd = fmt.Sprintf("%d", secs)
		}
	case "last":
		secs, _ := strconv.Atoi(r.FormValue("trim_secs"))
		if secs > 0 {
			job.TrimLastSecs = secs
		}
	}

	job.Mode = mode
	job.VideoItag = videoItag
	job.AudioItag = audioItag
	job.Status = model.StatusQueued
	job.WorkerID = "" // clear so any worker can pick it up
	if err := s.db.UpdateJob(job); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	log.Printf("job %s: format selected mode=%s video=%s audio=%s", id, mode, videoItag, audioItag)
	http.Redirect(w, r, "/job/"+id, http.StatusSeeOther)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, err := s.db.GetJob(id)
	if err != nil || job == nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	if job.Status != model.StatusDone || job.FilePath == "" {
		http.Error(w, "file not ready", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": job.FileName}))
	http.ServeFile(w, r, job.FilePath)
}

func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, err := s.db.GetJob(id)
	if err != nil || job == nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	switch job.Status {
	case model.StatusPending, model.StatusReady, model.StatusQueued, model.StatusRunning:
		job.Status = model.StatusCanceled
		if err := s.db.UpdateJob(job); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		log.Printf("job %s canceled by user", id)
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleJobStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, err := s.db.GetJob(id)
	if err != nil || job == nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	resp := model.StatusResp{Job: job}
	if job.Status == model.StatusReady {
		resp.Formats, _ = s.db.GetFormats(id)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// bestAudioItag picks the best audio format compatible with the selected video.
func (s *Server) bestAudioItag(jobID string, videoItag string) string {
	formats, err := s.db.GetFormats(jobID)
	if err != nil {
		return ""
	}

	// Find video container
	var videoContainer string
	for _, f := range formats {
		if f.Itag == videoItag {
			videoContainer = f.Container
			break
		}
	}

	// Compatible audio container: mp4→m4a, webm→webm/opus
	var best model.Format
	for _, f := range formats {
		if f.Type != "audio" {
			continue
		}
		compatible := false
		switch videoContainer {
		case "mp4":
			compatible = f.Container == "m4a" || f.Container == "mp4"
		case "webm":
			compatible = f.Container == "webm" || f.Container == "opus"
		default:
			compatible = true
		}
		if compatible && f.Bitrate > best.Bitrate {
			best = f
		}
	}
	// Fallback: just pick highest bitrate audio
	if best.Itag == "" {
		for _, f := range formats {
			if f.Type == "audio" && f.Bitrate > best.Bitrate {
				best = f
			}
		}
	}
	return best.Itag
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// validTimestamp checks that s is a reasonable HH:MM:SS / MM:SS / SS timestamp.
func validTimestamp(s string) bool {
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return false
	}
	for _, p := range parts {
		if len(p) == 0 || len(p) > 2 {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// clientIP extracts the real client IP, preferring X-Real-IP (set by nginx)
// over X-Forwarded-For, falling back to the direct connection address.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return strings.TrimSpace(ip)
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		// X-Forwarded-For may be a comma-separated list; the first entry is the client.
		if i := strings.IndexByte(fwd, ','); i >= 0 {
			return strings.TrimSpace(fwd[:i])
		}
		return strings.TrimSpace(fwd)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isPrivateHost returns true if the host resolves to a private/reserved IP address.
func isPrivateHost(host string) bool {
	// Strip port if present
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}

	// Block common private/internal hostnames
	lower := strings.ToLower(hostname)
	if lower == "localhost" || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") {
		return true
	}

	ips, err := net.LookupIP(hostname)
	if err != nil {
		// Can't resolve — block to be safe
		return true
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return true
		}
	}
	return false
}

// sanitizeYouTubeURL extracts a clean video URL, stripping playlist/radio params.
func sanitizeYouTubeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	// Normalize m.youtube.com → www.youtube.com
	if u.Host == "m.youtube.com" {
		u.Host = "www.youtube.com"
	}
	// Keep only the v parameter
	v := u.Query().Get("v")
	if v != "" {
		u.RawQuery = "v=" + v
	}
	return u.String()
}
