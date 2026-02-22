package controller

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"youdl/internal/model"
)

func (s *Server) handleWorkerProxies(w http.ResponseWriter, r *http.Request) {
	var proxies []string
	if s.cfg.ProxyListFile != "" {
		data, err := os.ReadFile(s.cfg.ProxyListFile)
		if err != nil {
			log.Printf("read proxy list: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				proxies = append(proxies, line)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string][]string{"proxies": proxies})
}

func (s *Server) handleWorkerCookies(w http.ResponseWriter, r *http.Request) {
	site := r.PathValue("site")
	path, ok := s.cfg.CookieFiles[site]
	if !ok || path == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.ServeFile(w, r, path)
}

func (s *Server) handleWorkerRegister(w http.ResponseWriter, r *http.Request) {
	var req model.WorkerRegisterReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		http.Error(w, "worker id required", http.StatusBadRequest)
		return
	}
	if req.MaxJobs < 1 {
		req.MaxJobs = 2
	}
	info := &model.WorkerInfo{
		ID:       req.ID,
		Addr:     req.Addr,
		MaxJobs:  req.MaxJobs,
		LastSeen: time.Now().UTC(),
	}
	if err := s.db.UpsertWorker(info); err != nil {
		log.Printf("register worker %s: %v", req.ID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("worker registered: %s (max_jobs=%d)", req.ID, req.MaxJobs)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleWorkerHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.db.WorkerHeartbeat(req.ID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleWorkerPoll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	job, err := s.AssignJob(req.ID)
	if err != nil {
		log.Printf("assign job for %s: %v", req.ID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(model.PollResp{Job: job})
}

func (s *Server) handleWorkerJobUpdate(w http.ResponseWriter, r *http.Request) {
	var req model.JobUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if req.Status == model.StatusFailed {
		if err := s.HandleJobFailure(req.JobID, req.WorkerID, req.Error); err != nil {
			log.Printf("handle failure for job %s: %v", req.JobID, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	job, err := s.db.GetJob(req.JobID)
	if err != nil || job == nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	job.Status = req.Status
	if req.Error != "" {
		job.Error = req.Error
	}
	if err := s.db.UpdateJob(job); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleWorkerJobMetadata(w http.ResponseWriter, r *http.Request) {
	var meta model.JobMetadata
	if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	job, err := s.db.GetJob(meta.JobID)
	if err != nil || job == nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	job.Title = meta.Title

	if err := s.db.SaveFormats(meta.JobID, meta.Formats); err != nil {
		log.Printf("save formats for job %s: %v", meta.JobID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Count format types
	var nVideo, nAudio, nMuxed int
	var bestMuxed model.Format
	for _, f := range meta.Formats {
		switch f.Type {
		case "video":
			nVideo++
		case "audio":
			nAudio++
		case "muxed":
			nMuxed++
			if f.Height > bestMuxed.Height {
				bestMuxed = f
			}
		}
	}

	if nVideo > 0 && nAudio > 0 {
		// Has separate streams (YouTube-style) — show format picker
		job.Status = model.StatusReady
		log.Printf("metadata received for job %s: %q (%d video, %d audio, %d muxed)", meta.JobID, meta.Title, nVideo, nAudio, nMuxed)
	} else if bestMuxed.Itag != "" {
		// Only muxed formats (Twitter/X) — auto-select best and queue
		job.Mode = model.ModeVideoOnly
		job.VideoItag = bestMuxed.Itag
		job.Status = model.StatusQueued
		job.WorkerID = ""
		log.Printf("metadata received for job %s: %q (auto-queued muxed %s)", meta.JobID, meta.Title, bestMuxed.Itag)
	} else {
		// No usable formats parsed — let yt-dlp pick best
		job.Mode = model.ModeBest
		job.Status = model.StatusQueued
		job.WorkerID = ""
		log.Printf("metadata received for job %s: %q (0 usable formats, auto-queued best)", meta.JobID, meta.Title)
	}

	if err := s.db.UpdateJob(job); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleWorkerJobUpload(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	job, err := s.db.GetJob(jobID)
	if err != nil || job == nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	// Read filename from query or header
	filename := r.URL.Query().Get("filename")
	if filename == "" {
		filename = fmt.Sprintf("%s.mp4", jobID)
	}

	destDir := filepath.Join(s.cfg.StorageDir, jobID)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	destPath := filepath.Join(destDir, filepath.Base(filename))
	f, err := os.Create(destPath)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	if _, err := io.Copy(f, r.Body); err != nil {
		os.Remove(destPath)
		http.Error(w, "upload failed", http.StatusInternalServerError)
		return
	}

	job.FilePath = destPath
	job.FileName = filepath.Base(filename)
	job.Status = model.StatusDone
	switch r.URL.Query().Get("proxy_line") {
	case "", "0":
		job.ProxyNote = "direct"
	default:
		job.ProxyNote = "proxy " + r.URL.Query().Get("proxy_line")
	}
	if err := s.db.UpdateJob(job); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	log.Printf("upload complete for job %s: %s", jobID, filename)
	w.WriteHeader(http.StatusOK)
}
