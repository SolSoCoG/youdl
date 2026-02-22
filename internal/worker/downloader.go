package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"youdl/internal/model"
)

type Downloader struct {
	throttle    func()
	outputDir   string
	proxies     []string          // rotation list
	cookieFiles map[string]string // site → file path
	maxDuration int               // seconds, 0 = unlimited
}

func NewDownloader(throttleFn func(), outputDir string, proxies []string, cookieFiles map[string]string, maxDuration int) *Downloader {
	for site, path := range cookieFiles {
		log.Printf("using %s cookies: %s", site, path)
	}
	if len(proxies) > 0 {
		log.Printf("proxy rotation enabled: %d proxies", len(proxies))
	}
	if maxDuration > 0 {
		log.Printf("max video duration: %d min", maxDuration/60)
	}
	return &Downloader{
		throttle:    throttleFn,
		outputDir:   outputDir,
		proxies:     proxies,
		cookieFiles: cookieFiles,
		maxDuration: maxDuration,
	}
}

// cookieForURL returns the cookie file path appropriate for the given URL, or "".
func (d *Downloader) cookieForURL(videoURL string) string {
	switch {
	case strings.Contains(videoURL, "youtube.com") || strings.Contains(videoURL, "youtu.be"):
		return d.cookieFiles["youtube"]
	case strings.Contains(videoURL, "reddit.com") || strings.Contains(videoURL, "redd.it"):
		return d.cookieFiles["reddit"]
	default:
		return ""
	}
}

// ytdlpJSON is the subset of yt-dlp -j output we care about.
type ytdlpJSON struct {
	Title      string        `json:"title"`
	Duration   float64       `json:"duration"`
	IsLive     bool          `json:"is_live"`
	LiveStatus string        `json:"live_status"` // "is_live", "is_upcoming", "was_live", "not_live"
	Formats    []ytdlpFormat `json:"formats"`
}

type ytdlpFormat struct {
	FormatID   string  `json:"format_id"`
	Ext        string  `json:"ext"`
	VCodec     string  `json:"vcodec"`
	ACodec     string  `json:"acodec"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	FPS        float64 `json:"fps"`
	TBR        float64 `json:"tbr"`  // total bitrate kbps
	VBR        float64 `json:"vbr"`
	ABR        float64 `json:"abr"`
	Resolution string  `json:"resolution"`
	FormatNote string  `json:"format_note"`
}

// FetchMetadata gets video info and returns available formats.
func (d *Downloader) FetchMetadata(ctx context.Context, videoURL string) (*model.JobMetadata, error) {
	d.throttle()

	// Use a permissive format selector so the JSON dump doesn't fail on
	// format selection before we even see the formats list.
	// The "formats" array in the output is always complete regardless of -f.
	args := append(d.baseArgs(), d.proxyArgs()...)
	args = append(args, d.cookieArgs(videoURL)...)
	args = append(args, "-j", "-f", "bestvideo+bestaudio/best/b")
	if isTwitterURL(videoURL) {
		args = append(args, "--extractor-args", "twitter:api=syndication")
	}
	args = append(args, videoURL)

	log.Printf("yt-dlp metadata cmd: yt-dlp %s", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("yt-dlp: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("yt-dlp: %w", err)
	}

	var info ytdlpJSON
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("parse yt-dlp output: %w", err)
	}

	// Reject active and upcoming livestreams. Completed livestreams (was_live)
	// are VODs and are allowed, subject to the duration limit below.
	if info.IsLive || info.LiveStatus == "is_live" || info.LiveStatus == "is_upcoming" {
		return nil, fmt.Errorf("live and upcoming streams are not supported")
	}

	// Reject videos that exceed the configured duration limit.
	if d.maxDuration > 0 && info.Duration > float64(d.maxDuration) {
		got := int(info.Duration / 60)
		limit := d.maxDuration / 60
		return nil, fmt.Errorf("video is %d min, which exceeds the %d min limit", got, limit)
	}

	meta := &model.JobMetadata{
		Title: info.Title,
	}

	seen := make(map[string]bool)
	for _, f := range info.Formats {
		fid := f.FormatID
		if fid == "" || seen[fid] {
			continue
		}
		seen[fid] = true

		mf := model.Format{
			Itag:   fid,
			Width:  f.Width,
			Height: f.Height,
			FPS:    int(f.FPS),
		}

		// Determine type
		hasVideo := f.VCodec != "" && f.VCodec != "none"
		hasAudio := f.ACodec != "" && f.ACodec != "none"

		if hasVideo && !hasAudio {
			mf.Type = "video"
			mf.Bitrate = int(f.VBR * 1000)
		} else if hasAudio && !hasVideo {
			mf.Type = "audio"
			mf.Bitrate = int(f.ABR * 1000)
		} else if hasVideo && hasAudio {
			mf.Type = "muxed"
			mf.Bitrate = int(f.TBR * 1000)
		} else if f.Height > 0 || f.TBR > 0 {
			// Null codecs but has resolution/bitrate — treat as muxed
			mf.Type = "muxed"
			mf.Bitrate = int(f.TBR * 1000)
		} else {
			continue
		}

		// Container
		mf.Container = f.Ext
		mf.MimeType = fmt.Sprintf("%s/%s", mf.Type, f.Ext)

		// Quality label
		if f.FormatNote != "" {
			mf.Quality = f.FormatNote
		} else if f.Height > 0 {
			mf.Quality = fmt.Sprintf("%dp", f.Height)
		}

		// Human-readable label
		switch mf.Type {
		case "video":
			if mf.FPS > 0 {
				mf.Label = fmt.Sprintf("%s %s %dfps %s", mf.Quality, mf.Container, mf.FPS, formatBitrate(mf.Bitrate))
			} else {
				mf.Label = fmt.Sprintf("%s %s %s", mf.Quality, mf.Container, formatBitrate(mf.Bitrate))
			}
		case "muxed":
			mf.Label = fmt.Sprintf("%s %s %s", mf.Quality, mf.Container, formatBitrate(mf.Bitrate))
		default:
			mf.Label = fmt.Sprintf("MP3 %s", formatBitrate(mf.Bitrate))
		}

		if mf.Bitrate == 0 && mf.Quality == "" {
			continue
		}

		meta.Formats = append(meta.Formats, mf)
	}

	log.Printf("parsed %d formats from yt-dlp (%d raw)", len(meta.Formats), len(info.Formats))
	return meta, nil
}

// Download handles all download modes using yt-dlp.
// Returns the local file path, the 1-based proxy line number used (0 = no proxy), and any error.
func (d *Downloader) Download(ctx context.Context, videoURL string, title string, mode string, videoItag, audioItag string) (string, int, error) {
	d.throttle()

	safeName := sanitizeFilename(title)
	outTemplate := filepath.Join(d.outputDir, safeName+".%(ext)s")
	pattern := filepath.Join(d.outputDir, safeName+".*")

	proxyA, proxyLine := d.proxyArgsWithIndex()
	args := append(d.baseArgs(), proxyA...)
	args = append(args, d.cookieArgs(videoURL)...)
	args = append(args, "--no-playlist", "-o", outTemplate)

	switch mode {
	case model.ModeBest:
		// Let yt-dlp pick the best format automatically
		args = append(args, "--merge-output-format", "mp4")
	case model.ModeVideoAudio:
		args = append(args, "-f", videoItag+"+"+audioItag, "--merge-output-format", "mp4")
	case model.ModeVideoOnly:
		args = append(args, "-f", videoItag, "--merge-output-format", "mp4")
	case model.ModeAudioOnly:
		args = append(args, "-f", audioItag, "--extract-audio", "--audio-format", "mp3")
	default:
		return "", 0, fmt.Errorf("unknown mode: %s", mode)
	}
	args = append(args, videoURL)

	if isTwitterURL(videoURL) {
		args = append(args, "--extractor-args", "twitter:api=syndication")
	}

	log.Printf("yt-dlp %s", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		// yt-dlp sometimes exits non-zero even when the output file was
		// successfully created (e.g. a thumbnail or subtitle step failed).
		// Check for the file before giving up.
		if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
			log.Printf("warning: yt-dlp exited with error but output file found (%s): %v", matches[0], err)
			return matches[0], proxyLine, nil
		}
		return "", 0, fmt.Errorf("yt-dlp download: %w", err)
	}

	// Find the output file (yt-dlp resolves %(ext)s)
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return "", 0, fmt.Errorf("output file not found for pattern %s", pattern)
	}

	log.Printf("downloaded: %s", matches[0])
	return matches[0], proxyLine, nil
}

// TrimFile cuts filePath with ffmpeg according to the trim parameters and returns
// the path to the trimmed file. If no trim is needed it returns filePath unchanged.
func (d *Downloader) TrimFile(ctx context.Context, filePath, trimStart, trimEnd string, trimLastSecs int) (string, error) {
	start := trimStart
	end := trimEnd

	if trimLastSecs > 0 {
		dur, err := probeDuration(ctx, filePath)
		if err != nil {
			return "", fmt.Errorf("probe duration: %w", err)
		}
		startSecs := dur - float64(trimLastSecs)
		if startSecs < 0 {
			startSecs = 0
		}
		start = fmt.Sprintf("%.3f", startSecs)
		end = ""
	}

	if start == "" && end == "" {
		return filePath, nil
	}

	ext := filepath.Ext(filePath)
	outPath := strings.TrimSuffix(filePath, ext) + "_cut" + ext

	args := []string{"-y"}
	if start != "" {
		args = append(args, "-ss", start)
	}
	if end != "" {
		args = append(args, "-to", end)
	}
	args = append(args, "-i", filePath, "-c", "copy", "-avoid_negative_ts", "make_zero", outPath)

	log.Printf("ffmpeg trim: ffmpeg %s", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg trim: %w", err)
	}
	return outPath, nil
}

// probeDuration returns the duration of a media file in seconds using ffprobe.
func probeDuration(ctx context.Context, filePath string) (float64, error) {
	out, err := exec.CommandContext(ctx, "ffprobe",
		"-v", "quiet",
		"-show_entries", "format=duration",
		"-of", "csv=p=0",
		filePath,
	).Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

func (d *Downloader) baseArgs() []string {
	return []string{
		"--no-warnings",
		"--js-runtimes", "node",
		"--remote-components", "ejs:github",
	}
}

// proxyArgsWithIndex picks a random proxy and returns the yt-dlp args and the
// 1-based line number (0 if no proxies configured).
func (d *Downloader) proxyArgsWithIndex() ([]string, int) {
	if len(d.proxies) == 0 {
		return nil, 0
	}
	idx := rand.Intn(len(d.proxies))
	proxy := d.proxies[idx]
	log.Printf("using proxy: %s", redactProxy(proxy))
	return []string{"--proxy", proxy}, idx + 1
}

func (d *Downloader) proxyArgs() []string {
	args, _ := d.proxyArgsWithIndex()
	return args
}

func (d *Downloader) cookieArgs(videoURL string) []string {
	if cf := d.cookieForURL(videoURL); cf != "" {
		return []string{"--cookies", cf}
	}
	return nil
}

// redactProxy hides credentials in a proxy URL for safe logging.
func redactProxy(s string) string {
	if i := strings.LastIndex(s, "@"); i >= 0 {
		scheme := ""
		if j := strings.Index(s, "://"); j >= 0 {
			scheme = s[:j+3]
		}
		return scheme + "***@" + s[i+1:]
	}
	return s
}

func formatBitrate(bps int) string {
	if bps > 1_000_000 {
		return fmt.Sprintf("%.1fMbps", float64(bps)/1_000_000)
	}
	if bps == 0 {
		return ""
	}
	return fmt.Sprintf("%dkbps", bps/1000)
}

func isTwitterURL(rawURL string) bool {
	return strings.Contains(rawURL, "twitter.com") || strings.Contains(rawURL, "x.com")
}

func sanitizeFilename(name string) string {
	// Strip null bytes and path separators
	s := strings.ReplaceAll(name, "\x00", "")
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_",
	)
	s = replacer.Replace(s)
	s = strings.TrimSpace(s)
	if len(s) > 100 {
		s = s[:100]
	}
	// Block . and .. traversal
	if s == "" || s == "." || s == ".." {
		s = "download"
	}
	return s
}
