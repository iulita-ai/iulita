package craft

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Client provides access to the Craft REST API.
type Client struct {
	mu         sync.RWMutex
	apiBaseURL string // e.g. https://connect.craft.do/links/XXX/api/v1
	apiKey     string // Bearer token
	httpClient *http.Client
}

// NewClient creates a Craft API client with the given base URL and API key.
func NewClient(apiBaseURL, apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		apiBaseURL: strings.TrimRight(apiBaseURL, "/"),
		apiKey:     apiKey,
		httpClient: httpClient,
	}
}

// UpdateCredentials updates the API URL and key at runtime (hot-reload).
func (c *Client) UpdateCredentials(apiURL, apiKey string) {
	c.mu.Lock()
	c.apiBaseURL = strings.TrimRight(apiURL, "/")
	c.apiKey = apiKey
	c.mu.Unlock()
}

func (c *Client) getCredentials() (baseURL, key string) {
	c.mu.RLock()
	baseURL, key = c.apiBaseURL, c.apiKey
	c.mu.RUnlock()
	return
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, result any) error {
	baseURL, apiKey := c.getCredentials()
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshaling request body: %w", err)
		}
		bodyReader = strings.NewReader(string(data))
	}

	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}

// doMarkdown performs a request expecting text/markdown response.
func (c *Client) doMarkdown(ctx context.Context, method, path string) (string, error) {
	baseURL, apiKey := c.getCredentials()
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "text/markdown")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

// --- Data types ---

// Document represents a Craft document.
type Document struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Folder represents a Craft folder.
type Folder struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	DocumentCount int      `json:"documentCount"`
	Subfolders    []Folder `json:"folders,omitempty"`
}

// SearchResult represents a search hit.
type SearchResult struct {
	DocumentID string `json:"documentId"`
	Markdown   string `json:"markdown"`
}

// TaskInfo contains task state metadata.
type TaskInfo struct {
	State        string `json:"state"` // todo, done, canceled
	ScheduleDate string `json:"scheduleDate,omitempty"`
}

// Task represents a Craft task.
type Task struct {
	ID       string   `json:"id"`
	Markdown string   `json:"markdown"`
	TaskInfo TaskInfo `json:"taskInfo"`
}

// --- API methods ---

// SearchDocuments searches across all documents.
func (c *Client) SearchDocuments(ctx context.Context, query string) ([]SearchResult, error) {
	u := fmt.Sprintf("/documents/search?include=%s", url.QueryEscape(query))
	var resp struct {
		Items []SearchResult `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// ListDocuments lists documents, optionally filtered by folder.
func (c *Client) ListDocuments(ctx context.Context, folderID string) ([]Document, error) {
	path := "/documents"
	if folderID != "" {
		path += "?folderId=" + url.QueryEscape(folderID)
	}
	var resp struct {
		Items []Document `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// ReadDocument reads document content as markdown via blocks endpoint.
func (c *Client) ReadDocument(ctx context.Context, documentID string) (string, error) {
	path := fmt.Sprintf("/blocks?id=%s&maxDepth=-1", url.QueryEscape(documentID))
	return c.doMarkdown(ctx, http.MethodGet, path)
}

// ListFolders returns the folder hierarchy.
func (c *Client) ListFolders(ctx context.Context) ([]Folder, error) {
	var resp struct {
		Items []Folder `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/folders", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// CreateDocument creates a new document with markdown content.
func (c *Client) CreateDocument(ctx context.Context, title, markdown, folderID string) (string, error) {
	doc := map[string]any{
		"title":   title,
		"content": markdown,
	}

	body := map[string]any{
		"documents": []any{doc},
	}
	if folderID != "" {
		body["folderId"] = folderID
	} else {
		body["folderId"] = "unsorted"
	}

	var resp struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/documents", body, &resp); err != nil {
		return "", err
	}
	if len(resp.Items) == 0 {
		return "", fmt.Errorf("no document ID returned")
	}
	return resp.Items[0].ID, nil
}

// AppendToDocument adds markdown content to an existing document.
func (c *Client) AppendToDocument(ctx context.Context, documentID, markdown string) error {
	body := map[string]any{
		"documentId": documentID,
		"markdown":   markdown,
		"position": map[string]any{
			"position": "end",
			"pageId":   documentID,
		},
	}
	return c.doJSON(ctx, http.MethodPost, "/blocks", body, nil)
}

// GetTasks retrieves tasks by scope.
func (c *Client) GetTasks(ctx context.Context, scope string) ([]Task, error) {
	if scope == "" {
		scope = "active"
	}
	path := fmt.Sprintf("/tasks?scope=%s", url.QueryEscape(scope))
	var resp struct {
		Items []Task `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// CreateTask creates a new task in inbox.
func (c *Client) CreateTask(ctx context.Context, content, scheduleDate string) (string, error) {
	task := map[string]any{
		"markdown": content,
		"location": map[string]string{"type": "inbox"},
	}
	if scheduleDate != "" {
		task["taskInfo"] = map[string]string{"scheduleDate": scheduleDate}
	}

	body := map[string]any{
		"tasks": []any{task},
	}

	var resp struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/tasks", body, &resp); err != nil {
		return "", err
	}
	if len(resp.Items) == 0 {
		return "", fmt.Errorf("no task ID returned")
	}
	return resp.Items[0].ID, nil
}

// UpdateTask updates a task's state.
func (c *Client) UpdateTask(ctx context.Context, taskID, state string) error {
	body := map[string]any{
		"tasksToUpdate": []map[string]any{
			{
				"id":       taskID,
				"taskInfo": map[string]string{"state": state},
			},
		},
	}
	return c.doJSON(ctx, http.MethodPut, "/tasks", body, nil)
}
