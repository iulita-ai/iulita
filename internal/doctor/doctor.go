package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// CheckResult represents a single health check result.
type CheckResult struct {
	Name    string
	Status  string // "OK", "WARN", "FAIL"
	Details string
}

// Doctor performs health checks on all system components.
type Doctor struct {
	checks []func(ctx context.Context) CheckResult
}

// New creates a new Doctor instance.
func New() *Doctor {
	return &Doctor{}
}

// AddCheck registers a health check function.
func (d *Doctor) AddCheck(fn func(ctx context.Context) CheckResult) {
	d.checks = append(d.checks, fn)
}

// RunAll executes all registered health checks and returns results.
func (d *Doctor) RunAll(ctx context.Context) []CheckResult {
	results := make([]CheckResult, 0, len(d.checks))
	for _, check := range d.checks {
		results = append(results, check(ctx))
	}
	return results
}

// PrintResults formats and writes check results to the given writer.
func (d *Doctor) PrintResults(w io.Writer, results []CheckResult) {
	for _, r := range results {
		icon := "[ OK ]"
		switch r.Status {
		case "WARN":
			icon = "[WARN]"
		case "FAIL":
			icon = "[FAIL]"
		}
		if r.Details != "" {
			fmt.Fprintf(w, "%s %s: %s\n", icon, r.Name, r.Details)
		} else {
			fmt.Fprintf(w, "%s %s\n", icon, r.Name)
		}
	}
}

// CheckSQLite returns a check that verifies the SQLite database is accessible.
func CheckSQLite(dbPath string) func(ctx context.Context) CheckResult {
	return func(_ context.Context) CheckResult {
		db, err := sql.Open("sqlite3", dbPath)
		if err != nil {
			return CheckResult{Name: "SQLite", Status: "FAIL", Details: err.Error()}
		}
		defer db.Close()

		if err := db.Ping(); err != nil {
			return CheckResult{Name: "SQLite", Status: "FAIL", Details: err.Error()}
		}
		return CheckResult{Name: "SQLite", Status: "OK", Details: dbPath}
	}
}

// CheckTelegram returns a check that calls the Telegram getMe API.
func CheckTelegram(token string) func(ctx context.Context) CheckResult {
	return func(ctx context.Context) CheckResult {
		if token == "" {
			return CheckResult{Name: "Telegram", Status: "WARN", Details: "no token configured"}
		}

		url := fmt.Sprintf("https://api.telegram.org/bot%s/getMe", token)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return CheckResult{Name: "Telegram", Status: "FAIL", Details: err.Error()}
		}

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return CheckResult{Name: "Telegram", Status: "FAIL", Details: err.Error()}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return CheckResult{Name: "Telegram", Status: "FAIL", Details: fmt.Sprintf("HTTP %d", resp.StatusCode)}
		}
		return CheckResult{Name: "Telegram", Status: "OK"}
	}
}

// CheckClaude returns a check that verifies connectivity to the Claude API.
func CheckClaude(apiKey, model string) func(ctx context.Context) CheckResult {
	return func(ctx context.Context) CheckResult {
		if apiKey == "" {
			return CheckResult{Name: "Claude", Status: "WARN", Details: "no API key configured"}
		}

		// Minimal request to verify API key validity.
		body := fmt.Sprintf(`{"model":"%s","max_tokens":1,"messages":[{"role":"user","content":"ping"}]}`, model)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(body))
		if err != nil {
			return CheckResult{Name: "Claude", Status: "FAIL", Details: err.Error()}
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return CheckResult{Name: "Claude", Status: "FAIL", Details: err.Error()}
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
			return CheckResult{Name: "Claude", Status: "OK", Details: model}
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return CheckResult{Name: "Claude", Status: "FAIL", Details: "invalid API key"}
		}
		return CheckResult{Name: "Claude", Status: "WARN", Details: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
}

// CheckOllama returns a check that verifies Ollama is running by calling GET /api/tags.
func CheckOllama(url string) func(ctx context.Context) CheckResult {
	return func(ctx context.Context) CheckResult {
		if url == "" {
			return CheckResult{Name: "Ollama", Status: "WARN", Details: "not configured"}
		}

		endpoint := strings.TrimRight(url, "/") + "/api/tags"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return CheckResult{Name: "Ollama", Status: "FAIL", Details: err.Error()}
		}

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return CheckResult{Name: "Ollama", Status: "FAIL", Details: err.Error()}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return CheckResult{Name: "Ollama", Status: "FAIL", Details: fmt.Sprintf("HTTP %d", resp.StatusCode)}
		}
		return CheckResult{Name: "Ollama", Status: "OK", Details: url}
	}
}

// CheckDashboard returns a check that verifies the dashboard is reachable via TCP.
func CheckDashboard(address string) func(ctx context.Context) CheckResult {
	return func(_ context.Context) CheckResult {
		if address == "" {
			return CheckResult{Name: "Dashboard", Status: "WARN", Details: "not configured"}
		}

		conn, err := net.DialTimeout("tcp", address, 3*time.Second)
		if err != nil {
			return CheckResult{Name: "Dashboard", Status: "FAIL", Details: err.Error()}
		}
		conn.Close()
		return CheckResult{Name: "Dashboard", Status: "OK", Details: address}
	}
}
