package common

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// CookieSites lists all supported per-site cookie env vars (YOUDL_C_<SITE>).
var CookieSites = []string{"youtube", "reddit"}

type ControllerConfig struct {
	Listen        string
	AuthToken     string
	DBPath        string
	StorageDir    string
	JobTTL        time.Duration
	CookieFiles   map[string]string // site → local file path
	ProxyListFile string            // path to file with one proxy per line
	RateLimit     int               // max /submit requests per IP per minute, 0 = disabled
	MaxJobsPerIP  int               // max simultaneous active jobs per IP, 0 = disabled
	MaxQueueDepth int               // max total active jobs across all IPs, 0 = disabled
}

type WorkerConfig struct {
	ControllerURL string
	AuthToken     string
	WorkerID      string
	MaxJobs       int
	PollInterval  time.Duration
	Throttle      time.Duration
	MaxDuration   int   // max video duration in seconds, 0 = unlimited
	UploadLimit   int64 // bytes per second, 0 = unlimited
}

// resolveToken reads the auth token from (in order):
// 1. YOUDL_AUTH_TOKEN env var
// 2. .youdl config file (key=value, looks for auth_token)
func resolveToken() string {
	if v := os.Getenv("YOUDL_AUTH_TOKEN"); v != "" {
		return v
	}
	return readConfigValue("auth_token")
}

func LoadControllerConfig() (*ControllerConfig, error) {
	token := resolveToken()
	if token == "" {
		token = generateToken()
		log.Printf("generated auth token: %s", token)
		log.Println("put this in .youdl (auth_token=<token>) or YOUDL_AUTH_TOKEN on your workers")
	}
	jobTTL, err := time.ParseDuration(envOr("YOUDL_JOB_TTL", "30m"))
	if err != nil {
		jobTTL = 30 * time.Minute
	}
	cookieFiles := make(map[string]string)
	for _, site := range CookieSites {
		if path := os.Getenv("YOUDL_C_" + strings.ToUpper(site)); path != "" {
			cookieFiles[site] = path
		}
	}
	rateLimit, _ := strconv.Atoi(envOr("YOUDL_RATE_LIMIT", "10"))
	maxJobsPerIP, _ := strconv.Atoi(envOr("YOUDL_MAX_JOBS_PER_IP", "5"))
	maxQueueDepth, _ := strconv.Atoi(envOr("YOUDL_MAX_QUEUE_DEPTH", "0"))

	return &ControllerConfig{
		Listen:        envOr("YOUDL_LISTEN", ":8080"),
		AuthToken:     token,
		DBPath:        envOr("YOUDL_DB_PATH", "youdl.db"),
		StorageDir:    envOr("YOUDL_STORAGE_DIR", "storage"),
		JobTTL:        jobTTL,
		CookieFiles:   cookieFiles,
		ProxyListFile: os.Getenv("YOUDL_PROXY_LIST"),
		RateLimit:     rateLimit,
		MaxJobsPerIP:  maxJobsPerIP,
		MaxQueueDepth: maxQueueDepth,
	}, nil
}

func LoadWorkerConfig() (*WorkerConfig, error) {
	token := resolveToken()
	if token == "" {
		return nil, fmt.Errorf("no auth token found: set YOUDL_AUTH_TOKEN or add auth_token=<token> to .youdl")
	}
	maxJobs, _ := strconv.Atoi(envOr("YOUDL_MAX_JOBS", "2"))
	if maxJobs < 1 {
		maxJobs = 2
	}
	poll, err := time.ParseDuration(envOr("YOUDL_POLL_INTERVAL", "5s"))
	if err != nil {
		poll = 5 * time.Second
	}
	throttle, err := time.ParseDuration(envOr("YOUDL_THROTTLE", "500ms"))
	if err != nil {
		throttle = 500 * time.Millisecond
	}
	hostname, _ := os.Hostname()
	workerID := envOr("YOUDL_WORKER_ID", hostname)
	if workerID == "" {
		workerID = fmt.Sprintf("worker-%d", os.Getpid())
	}
	uploadLimit := parseByteRate(envOr("YOUDL_UPLOAD_LIMIT", "0"))
	maxDuration, _ := strconv.Atoi(envOr("YOUDL_MAX_DURATION", "3600"))

	return &WorkerConfig{
		ControllerURL: envOr("YOUDL_CONTROLLER", "http://localhost:8080"),
		AuthToken:     token,
		WorkerID:      workerID,
		MaxJobs:       maxJobs,
		PollInterval:  poll,
		Throttle:      throttle,
		MaxDuration:   maxDuration,
		UploadLimit:   uploadLimit,
	}, nil
}

func generateToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// readConfigValue reads a value from the .youdl config file (simple key=value format).
func readConfigValue(key string) string {
	data, err := os.ReadFile(".youdl")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// parseByteRate parses a human-friendly byte rate like "1M", "500K", "1.5M".
// Supports K (kilobytes), M (megabytes). Returns bytes per second.
func parseByteRate(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0
	}
	s = strings.ToUpper(s)
	multiplier := int64(1)
	if strings.HasSuffix(s, "M") {
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "M")
	} else if strings.HasSuffix(s, "K") {
		multiplier = 1024
		s = strings.TrimSuffix(s, "K")
	}
	val, err := strconv.ParseFloat(s, 64)
	if err != nil || val <= 0 {
		return 0
	}
	return int64(val * float64(multiplier))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
