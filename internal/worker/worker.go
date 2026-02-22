package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"

	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"time"

	"youdl/internal/common"
	"youdl/internal/model"
)

type Worker struct {
	cfg        *common.WorkerConfig
	httpClient *http.Client
	downloader *Downloader
	tmpDir     string
}

func New(cfg *common.WorkerConfig) (*Worker, error) {
	tmpDir := filepath.Join(os.TempDir(), "youdl-worker-"+cfg.WorkerID)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, fmt.Errorf("create tmp dir: %w", err)
	}

	cookieFiles := fetchAllCookies(cfg, tmpDir)
	var proxies []string
	if os.Getenv("YOUDL_NO_PROXY") == "" {
		proxies = fetchProxies(cfg)
	} else {
		log.Printf("proxy fetch skipped (YOUDL_NO_PROXY set), using direct connection")
	}

	throttle := func() {
		time.Sleep(cfg.Throttle)
	}

	dl := NewDownloader(throttle, tmpDir, proxies, cookieFiles, cfg.MaxDuration)

	return &Worker{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 5 * time.Minute},
		downloader: dl,
		tmpDir:     tmpDir,
	}, nil
}

func fetchAllCookies(cfg *common.WorkerConfig, tmpDir string) map[string]string {
	client := &http.Client{Timeout: 30 * time.Second}
	result := make(map[string]string)
	for _, site := range common.CookieSites {
		url := cfg.ControllerURL + "/api/worker/cookies/" + site
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("could not fetch %s cookies: %v", site, err)
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || len(data) == 0 {
			continue
		}
		path := filepath.Join(tmpDir, "cookies_"+site+".txt")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			log.Printf("could not write %s cookies: %v", site, err)
			continue
		}
		result[site] = path
		log.Printf("fetched %s cookies from controller (%d bytes)", site, len(data))
	}
	return result
}

func fetchProxies(cfg *common.WorkerConfig) []string {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", cfg.ControllerURL+"/api/worker/proxies", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("could not fetch proxies: %v", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var result struct {
		Proxies []string `json:"proxies"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("could not parse proxy list: %v", err)
		return nil
	}
	if len(result.Proxies) > 0 {
		log.Printf("fetched %d proxies from controller", len(result.Proxies))
	}
	return result.Proxies
}

func (w *Worker) Run(ctx context.Context) error {
	if err := w.register(); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	log.Printf("worker %s registered with controller %s", w.cfg.WorkerID, w.cfg.ControllerURL)

	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-heartbeat.C:
			w.sendHeartbeat()
		case <-ticker.C:
			job, err := w.poll()
			if err != nil {
				log.Printf("poll error: %v", err)
				continue
			}
			if job == nil {
				continue
			}
			go w.processJob(ctx, job)
		}
	}
}

func (w *Worker) processJob(ctx context.Context, job *model.Job) {
	log.Printf("processing job %s (status=%s, url=%s)", job.ID, job.Status, job.URL)

	switch job.Status {
	case model.StatusPending:
		w.fetchMetadata(ctx, job)
	case model.StatusQueued, model.StatusRunning:
		w.downloadJob(ctx, job)
	default:
		log.Printf("unexpected job status: %s", job.Status)
	}
}

func (w *Worker) fetchMetadata(ctx context.Context, job *model.Job) {
	meta, err := w.downloader.FetchMetadata(ctx, job.URL)
	if err != nil {
		log.Printf("fetch metadata for %s: %v", job.ID, err)
		w.reportFailure(job.ID, err.Error())
		return
	}
	meta.JobID = job.ID

	if err := w.postJSON("/api/worker/job/metadata", meta); err != nil {
		log.Printf("report metadata for %s: %v", job.ID, err)
	}
}

func (w *Worker) downloadJob(ctx context.Context, job *model.Job) {
	w.reportStatus(job.ID, model.StatusRunning)

	finalPath, proxyLine, err := w.downloader.Download(ctx, job.URL, job.Title, job.Mode, job.VideoItag, job.AudioItag)
	if err != nil {
		log.Printf("download job %s: %v", job.ID, err)
		w.reportFailure(job.ID, err.Error())
		return
	}

	// Check if job was canceled while downloading
	if w.isJobCanceled(job.ID) {
		os.Remove(finalPath)
		log.Printf("job %s was canceled, discarding download", job.ID)
		return
	}

	// Trim if requested
	if job.TrimStart != "" || job.TrimEnd != "" || job.TrimLastSecs > 0 {
		trimmedPath, err := w.downloader.TrimFile(ctx, finalPath, job.TrimStart, job.TrimEnd, job.TrimLastSecs)
		if err != nil {
			log.Printf("trim job %s: %v", job.ID, err)
			w.reportFailure(job.ID, "trim failed: "+err.Error())
			os.Remove(finalPath)
			return
		}
		if trimmedPath != finalPath {
			os.Remove(finalPath)
			finalPath = trimmedPath
		}
	}

	if err := w.uploadFile(job.ID, finalPath, proxyLine); err != nil {
		log.Printf("upload job %s: %v", job.ID, err)
		w.reportFailure(job.ID, err.Error())
		return
	}

	os.Remove(finalPath)
	log.Printf("job %s complete", job.ID)
}

func (w *Worker) isJobCanceled(jobID string) bool {
	resp, err := http.Get(w.cfg.ControllerURL + "/api/job/" + jobID + "/status")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var status model.StatusResp
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return false
	}
	return status.Job != nil && status.Job.Status == model.StatusCanceled
}

// Controller API helpers

func (w *Worker) register() error {
	return w.postJSON("/api/worker/register", model.WorkerRegisterReq{
		ID:      w.cfg.WorkerID,
		MaxJobs: w.cfg.MaxJobs,
	})
}

func (w *Worker) sendHeartbeat() {
	w.postJSON("/api/worker/heartbeat", map[string]string{"id": w.cfg.WorkerID})
}

func (w *Worker) poll() (*model.Job, error) {
	body, err := w.doPost("/api/worker/poll", map[string]string{"id": w.cfg.WorkerID})
	if err != nil {
		return nil, err
	}
	var resp model.PollResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Job, nil
}

func (w *Worker) reportStatus(jobID, status string) {
	w.postJSON("/api/worker/job/update", model.JobUpdateReq{
		JobID:    jobID,
		Status:   status,
		WorkerID: w.cfg.WorkerID,
	})
}

func (w *Worker) reportFailure(jobID, errMsg string) {
	w.postJSON("/api/worker/job/update", model.JobUpdateReq{
		JobID:    jobID,
		Status:   model.StatusFailed,
		Error:    errMsg,
		WorkerID: w.cfg.WorkerID,
	})
}

func (w *Worker) uploadFile(jobID, filePath string, proxyLine int) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	filename := filepath.Base(filePath)
	url := fmt.Sprintf("%s/api/worker/job/%s/upload?filename=%s",
		w.cfg.ControllerURL, jobID, neturl.QueryEscape(filename))
	url += fmt.Sprintf("&proxy_line=%d", proxyLine)

	var body io.Reader = f
	if w.cfg.UploadLimit > 0 {
		body = newRateLimitedReader(f, w.cfg.UploadLimit)
	}

	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+w.cfg.AuthToken)
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed (%d): %s", resp.StatusCode, string(body))
	}
	return nil
}

func (w *Worker) postJSON(path string, payload any) error {
	_, err := w.doPost(path, payload)
	return err
}

func (w *Worker) doPost(path string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	url := w.cfg.ControllerURL + path
	req, err := http.NewRequest("POST", url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.cfg.AuthToken)

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API %s returned %d: %s", path, resp.StatusCode, string(body))
	}
	return body, nil
}
