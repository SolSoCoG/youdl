package controller

import (
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"youdl/internal/common"
)

type Server struct {
	db        *DB
	cfg       *common.ControllerConfig
	templates map[string]*template.Template
	mux       *http.ServeMux
	rl        *ipRateLimiter
}

func NewServer(cfg *common.ControllerConfig) (*Server, error) {
	db, err := NewDB(cfg.DBPath)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(cfg.StorageDir, 0o755); err != nil {
		return nil, err
	}

	s := &Server{
		db:  db,
		cfg: cfg,
		mux: http.NewServeMux(),
		rl:  newIPRateLimiter(cfg.RateLimit, time.Minute),
	}

	s.loadTemplates()
	s.routes()
	return s, nil
}

func (s *Server) loadTemplates() {
	candidates := []string{
		"web/templates",
		filepath.Join(filepath.Dir(os.Args[0]), "web/templates"),
	}
	var tmplDir string
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			tmplDir = c
			break
		}
	}
	if tmplDir == "" {
		log.Fatal("web/templates directory not found")
	}

	funcMap := template.FuncMap{
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"safeJS":  func(s string) template.JS { return template.JS(s) },
	}

	layoutFile := filepath.Join(tmplDir, "layout.html")
	pages := []string{"index.html", "job.html"}
	s.templates = make(map[string]*template.Template, len(pages))
	for _, p := range pages {
		s.templates[p] = template.Must(
			template.New("").Funcs(funcMap).ParseFiles(layoutFile, filepath.Join(tmplDir, p)),
		)
	}
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	t, ok := s.templates[name]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func (s *Server) routes() {
	// Static files
	staticCandidates := []string{"web/static", filepath.Join(filepath.Dir(os.Args[0]), "web/static")}
	for _, c := range staticCandidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			sub, _ := fs.Sub(os.DirFS(c), ".")
			s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(sub)))
			break
		}
	}

	// User-facing routes
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.mux.HandleFunc("POST /submit", s.handleSubmit)
	s.mux.HandleFunc("GET /job/{id}", s.handleJobPage)
	s.mux.HandleFunc("POST /job/{id}/select", s.handleFormatSelect)
	s.mux.HandleFunc("GET /job/{id}/download", s.handleDownload)
	s.mux.HandleFunc("POST /job/{id}/cancel", s.handleJobCancel)
	s.mux.HandleFunc("GET /api/job/{id}/status", s.handleJobStatus)

	// Worker-facing routes (auth required)
	workerMux := http.NewServeMux()
	workerMux.HandleFunc("POST /api/worker/register", s.handleWorkerRegister)
	workerMux.HandleFunc("POST /api/worker/heartbeat", s.handleWorkerHeartbeat)
	workerMux.HandleFunc("POST /api/worker/poll", s.handleWorkerPoll)
	workerMux.HandleFunc("POST /api/worker/job/update", s.handleWorkerJobUpdate)
	workerMux.HandleFunc("POST /api/worker/job/metadata", s.handleWorkerJobMetadata)
	workerMux.HandleFunc("POST /api/worker/job/{id}/upload", s.handleWorkerJobUpload)
	workerMux.HandleFunc("GET /api/worker/cookies/{site}", s.handleWorkerCookies)
	workerMux.HandleFunc("GET /api/worker/proxies", s.handleWorkerProxies)

	authed := common.BearerAuth(s.cfg.AuthToken, workerMux)
	s.mux.Handle("/api/worker/", authed)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// StartCleanup runs a background goroutine that deletes expired jobs and their files.
func (s *Server) StartCleanup() {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.cleanupExpired()
		}
	}()
	log.Printf("cleanup enabled: jobs expire after %s", s.cfg.JobTTL)
}

func (s *Server) cleanupExpired() {
	jobs, err := s.db.ExpiredJobs(s.cfg.JobTTL)
	if err != nil {
		log.Printf("cleanup query: %v", err)
		return
	}
	for _, j := range jobs {
		if j.FilePath != "" {
			os.Remove(j.FilePath)
			// Try removing the job directory too
			os.Remove(filepath.Dir(j.FilePath))
		}
		if err := s.db.DeleteJob(j.ID); err != nil {
			log.Printf("cleanup delete job %s: %v", j.ID, err)
		} else {
			log.Printf("cleaned up expired job %s", j.ID)
		}
	}
}

func (s *Server) Close() error {
	return s.db.Close()
}
