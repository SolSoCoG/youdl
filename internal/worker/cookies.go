package worker

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

// generateCookieFile visits youtube.com, accepts consent, and writes a
// Netscape-format cookie file for yt-dlp to use.
func generateCookieFile(outputDir string, proxy string) (string, error) {
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	}

	client := &http.Client{
		Jar:       jar,
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	// Step 1: Visit youtube.com to get initial cookies
	req, _ := http.NewRequest("GET", "https://www.youtube.com/", nil)
	req.Header.Set("User-Agent", randomUA())
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("visit youtube: %w", err)
	}
	resp.Body.Close()

	// Step 2: Set consent cookie directly (bypasses the "are you human" banner)
	consentVal := fmt.Sprintf("YES+cb.20210328-17-p0.en+FX+%d", 100+rand.Intn(899))
	ytURL, _ := url.Parse("https://www.youtube.com/")
	jar.SetCookies(ytURL, []*http.Cookie{
		{Name: "CONSENT", Value: consentVal, Domain: ".youtube.com", Path: "/"},
		{Name: "SOCS", Value: "CAISNQgDEitib3FfaWRlbnRpdHlmcm9udGVuZHVpc2VydmVyXzIwMjMwODI5LjA3X3AxGgJlbiACGgYIgJnSmgY", Domain: ".youtube.com", Path: "/"},
	})

	// Step 3: Visit again with consent cookies set
	req, _ = http.NewRequest("GET", "https://www.youtube.com/", nil)
	req.Header.Set("User-Agent", randomUA())
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err = client.Do(req)
	if err != nil {
		return "", fmt.Errorf("revisit youtube: %w", err)
	}
	resp.Body.Close()

	// Step 4: Write cookies in Netscape format
	cookiePath := filepath.Join(outputDir, "cookies.txt")
	allCookies := jar.Cookies(ytURL)

	var lines []string
	lines = append(lines, "# Netscape HTTP Cookie File")
	for _, c := range allCookies {
		httpOnly := "FALSE"
		secure := "FALSE"
		if c.Secure {
			secure = "TRUE"
		}
		if c.HttpOnly {
			httpOnly = "TRUE"
		}
		domain := c.Domain
		if domain == "" {
			domain = ".youtube.com"
		}
		expires := time.Now().Add(365 * 24 * time.Hour).Unix()
		line := fmt.Sprintf("%s\tTRUE\t%s\t%s\t%d\t%s\t%s",
			domain, c.Path, secure, expires, c.Name, c.Value)
		_ = httpOnly
		lines = append(lines, line)
	}

	if err := os.WriteFile(cookiePath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write cookies: %w", err)
	}

	log.Printf("generated cookie file with %d cookies", len(allCookies))
	return cookiePath, nil
}

var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.2 Safari/605.1.15",
}

func randomUA() string {
	return userAgents[rand.Intn(len(userAgents))]
}
