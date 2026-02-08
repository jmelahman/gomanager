package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// safeGoEnv returns a minimal environment for running go install on untrusted
// packages. Only variables required by the Go toolchain are included — secrets
// like GITHUB_TOKEN and CI runner tokens are explicitly excluded so a malicious
// package cannot exfiltrate them (e.g. via #cgo directives).
func safeGoEnv(gobin string, extra map[string]string) []string {
	// Allowlist of environment variables safe/needed for go install.
	allowed := []string{
		"HOME", "USER", "PATH", "TMPDIR",
		"GOPATH", "GOROOT", "GOMODCACHE", "GOPROXY", "GONOSUMCHECK",
		"GONOSUMDB", "GONOPROXY", "GOPRIVATE", "GOFLAGS", "GOTOOLCHAIN",
		"GOTELEMETRY", "SSL_CERT_FILE", "SSL_CERT_DIR",
		// Needed on some systems for DNS/TLS
		"LANG", "LC_ALL",
	}

	env := make([]string, 0, len(allowed)+len(extra)+1)
	for _, key := range allowed {
		if val, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+val)
		}
	}
	env = append(env, "GOBIN="+gobin)
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func tryGoInstall(installPath string, envFlags map[string]string) (ok bool, flags map[string]string, errMsg string) {
	tmpDir, err := os.MkdirTemp("", "gomanager-verify-*")
	if err != nil {
		return false, envFlags, fmt.Sprintf("cannot create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	goCmd := exec.Command("go", "install", installPath)
	goCmd.Env = safeGoEnv(tmpDir, envFlags)

	var stderr bytes.Buffer
	goCmd.Stderr = &stderr

	if err := goCmd.Run(); err != nil {
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		if len(lines) > 5 {
			lines = lines[:5]
		}
		return false, envFlags, strings.Join(lines, " ")
	}

	return true, envFlags, ""
}

func parseEnvFlags(flagsJSON string) map[string]string {
	if flagsJSON == "" || flagsJSON == "{}" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(flagsJSON), &m); err != nil {
		return nil
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func marshalFlags(flags map[string]string) string {
	if len(flags) == 0 {
		return "{}"
	}
	b, err := json.Marshal(flags)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func parseGitHubOwnerRepo(pkg string) (owner, repo string, ok bool) {
	if !strings.HasPrefix(pkg, "github.com/") {
		return "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(pkg, "github.com/"), "/", 3)
	if len(parts) < 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func fetchModulePath(client *http.Client, owner, repo, token string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/go.mod", owner, repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3.raw")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module")), nil
		}
	}
	return "", fmt.Errorf("no module directive found in go.mod")
}

func fetchLatestRelease(client *http.Client, owner, repo, token string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 403 || resp.StatusCode == 429 {
		retryAfter := resp.Header.Get("Retry-After")
		if retryAfter != "" {
			fmt.Printf("  Rate limited, waiting %ss...\n", retryAfter)
		} else {
			fmt.Println("  Rate limited, waiting 60s...")
		}
		time.Sleep(60 * time.Second)
		return "", fmt.Errorf("rate limited")
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}
	return release.TagName, nil
}
