// Package platform provides a lightweight HTTP client for the cnips mgmt-srv
// REST API used by the local pull command. It does NOT implement any OIDC
// login flow; callers supply the Bearer token directly.
package platform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// mgmt-srv caps list pagination at 20 in cnips-data-model/pkg/api.
const defaultPageSize = 20

// Client calls mgmt-srv REST endpoints.
type Client struct {
	baseURL    string
	tenantKey  string
	token      string
	httpClient *http.Client
}

// NewClient creates a client targeting baseURL (e.g. "http://localhost:8090").
// tenantKey is the optional "x-tenant-key" header value. Pass "" to let
// gateway/proxy routing resolve the tenant.
// token is an optional "Authorization: Bearer …" value. Pass "" to skip the
// header (only works when the local mgmt-srv has auth disabled).
func NewClient(baseURL, tenantKey, token string) *Client {
	return &Client{
		baseURL:   baseURL,
		tenantKey: tenantKey,
		token:     token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// -----------------------------------------------------------------------
// list helpers
// -----------------------------------------------------------------------

// ListPipelines fetches all pipelines for a workspace.
func (c *Client) ListPipelines(workspaceID string) ([]Pipeline, error) {
	return listAll[Pipeline](c, fmt.Sprintf("/workspace/%s/pipelines", workspaceID))
}

// ListSources fetches all sources for a workspace.
func (c *Client) ListSources(workspaceID string) ([]Source, error) {
	return listAll[Source](c, fmt.Sprintf("/workspace/%s/sources", workspaceID))
}

// ListTransformations fetches all transformations for a workspace.
func (c *Client) ListTransformations(workspaceID string) ([]Transformation, error) {
	return listAll[Transformation](c, fmt.Sprintf("/workspace/%s/transformations", workspaceID))
}

func (c *Client) GetTransformation(workspaceID, id string) (*Transformation, error) {
	var transformation Transformation
	if err := c.GetJSON(fmt.Sprintf("/workspace/%s/transformations/%s", workspaceID, url.PathEscape(id)), &transformation); err != nil {
		return nil, err
	}
	normalizeWriteID(&transformation)
	return &transformation, nil
}

// ListDestinations fetches all destinations for a workspace.
func (c *Client) ListDestinations(workspaceID string) ([]Destination, error) {
	return listAll[Destination](c, fmt.Sprintf("/workspace/%s/destinations", workspaceID))
}

func (c *Client) GetDestination(workspaceID, id string) (*Destination, error) {
	var destination Destination
	if err := c.GetJSON(fmt.Sprintf("/workspace/%s/destinations/%s", workspaceID, url.PathEscape(id)), &destination); err != nil {
		return nil, err
	}
	normalizeWriteID(&destination)
	return &destination, nil
}

// ListFunctions fetches all functions for a workspace.
func (c *Client) ListFunctions(workspaceID string) ([]Function, error) {
	return listAll[Function](c, fmt.Sprintf("/workspace/%s/functions", workspaceID))
}

// ListGlobalVariables fetches all global variables for a workspace.
func (c *Client) ListGlobalVariables(workspaceID string) ([]GlobalVariable, error) {
	return listAllFallback[GlobalVariable](c,
		fmt.Sprintf("/workspace/%s/globalvariable", workspaceID),
		fmt.Sprintf("/workspace/%s/variables", workspaceID),
	)
}

// ListPipelineGlobalVariables fetches all global variables attached to a pipeline.
func (c *Client) ListPipelineGlobalVariables(workspaceID, pipelineID string) ([]GlobalVariable, error) {
	return listAll[GlobalVariable](c, fmt.Sprintf("/workspace/%s/pipelines/%s/globalvariable", workspaceID, pipelineID))
}

// ListConfigurations fetches all configurations for a workspace.
func (c *Client) ListConfigurations(workspaceID string) ([]Configuration, error) {
	return listAll[Configuration](c, fmt.Sprintf("/workspace/%s/configurations", workspaceID))
}

// ListApps fetches all tenant/global apps. Apps are not scoped by workspace.
func (c *Client) ListApps() ([]App, error) {
	return listAll[App](c, "/apps")
}

// GetAppVersion fetches one app version, including its source payload.
func (c *Client) GetAppVersion(versionID string) (*AppVersion, error) {
	var version AppVersion
	if err := c.GetJSON(fmt.Sprintf("/appversions/%s", url.PathEscape(versionID)), &version); err != nil {
		return nil, err
	}
	normalizeWriteID(&version)
	return &version, nil
}

// ListVersions fetches all code versions for a component reference.
func (c *Client) ListVersions(workspaceID, ref, componentType string) ([]Version, error) {
	return listAll[Version](c, fmt.Sprintf("/workspace/%s/versions/ref/%s/type/%s", workspaceID, url.PathEscape(ref), url.PathEscape(componentType)))
}

// ListWorkspaces fetches workspaces accessible to the authenticated user.
func (c *Client) ListWorkspaces() ([]Workspace, error) {
	var workspaces []Workspace
	if err := c.GetJSON("/workspace", &workspaces); err != nil {
		return nil, err
	}
	return workspaces, nil
}

func (c *Client) CreateTransformation(workspaceID string, body Transformation) (*Transformation, error) {
	return postJSON[Transformation, Transformation](c, fmt.Sprintf("/workspace/%s/transformations", workspaceID), body)
}

func (c *Client) UpdateTransformation(workspaceID, id string, body Transformation) (*Transformation, error) {
	return putJSON[Transformation, Transformation](c, fmt.Sprintf("/workspace/%s/transformations/%s", workspaceID, url.PathEscape(id)), body)
}

func (c *Client) DeleteTransformation(workspaceID, id string) error {
	return deleteJSON(c, fmt.Sprintf("/workspace/%s/transformations/%s", workspaceID, url.PathEscape(id)))
}

func (c *Client) CreateSource(workspaceID string, body Source) (*Source, error) {
	return postJSON[Source, Source](c, fmt.Sprintf("/workspace/%s/sources", workspaceID), body)
}

func (c *Client) UpdateSource(workspaceID, id string, body Source) (*Source, error) {
	return putJSON[Source, Source](c, fmt.Sprintf("/workspace/%s/sources/%s", workspaceID, url.PathEscape(id)), body)
}

func (c *Client) DeleteSource(workspaceID, id string) error {
	return deleteJSON(c, fmt.Sprintf("/workspace/%s/sources/%s", workspaceID, url.PathEscape(id)))
}

func (c *Client) CreateDestination(workspaceID string, body Destination) (*Destination, error) {
	return postJSON[Destination, Destination](c, fmt.Sprintf("/workspace/%s/destinations", workspaceID), body)
}

func (c *Client) UpdateDestination(workspaceID, id string, body Destination) (*Destination, error) {
	return putJSON[Destination, Destination](c, fmt.Sprintf("/workspace/%s/destinations/%s", workspaceID, url.PathEscape(id)), body)
}

func (c *Client) DeleteDestination(workspaceID, id string) error {
	return deleteJSON(c, fmt.Sprintf("/workspace/%s/destinations/%s", workspaceID, url.PathEscape(id)))
}

func (c *Client) CreateFunction(workspaceID string, body Function) (*Function, error) {
	return postJSON[Function, Function](c, fmt.Sprintf("/workspace/%s/functions", workspaceID), body)
}

func (c *Client) UpdateFunction(workspaceID, id string, body Function) (*Function, error) {
	return putJSON[Function, Function](c, fmt.Sprintf("/workspace/%s/functions/%s", workspaceID, url.PathEscape(id)), body)
}

func (c *Client) DeleteFunction(workspaceID, id string) error {
	return deleteJSON(c, fmt.Sprintf("/workspace/%s/functions/%s", workspaceID, url.PathEscape(id)))
}

func (c *Client) CreateGlobalVariable(workspaceID string, body GlobalVariable) (*GlobalVariable, error) {
	return postJSON[GlobalVariable, GlobalVariable](c, fmt.Sprintf("/workspace/%s/globalvariable", workspaceID), body)
}

func (c *Client) UpdateGlobalVariable(workspaceID, id string, body GlobalVariable) (*GlobalVariable, error) {
	return putJSON[GlobalVariable, GlobalVariable](c, fmt.Sprintf("/workspace/%s/globalvariable/%s", workspaceID, url.PathEscape(id)), body)
}

func (c *Client) DeleteGlobalVariable(workspaceID, id string) error {
	if err := deleteJSON(c, fmt.Sprintf("/workspace/%s/globalvariable/%s", workspaceID, url.PathEscape(id))); err != nil {
		if isNotFoundError(err) {
			return deleteJSON(c, fmt.Sprintf("/workspace/%s/variables/%s", workspaceID, url.PathEscape(id)))
		}
		return err
	}
	return nil
}

func (c *Client) CreateConfiguration(workspaceID string, body Configuration) (*Configuration, error) {
	return postJSON[Configuration, Configuration](c, fmt.Sprintf("/workspace/%s/configurations", workspaceID), body)
}

func (c *Client) UpdateConfiguration(workspaceID, id string, body Configuration) (*Configuration, error) {
	return putJSON[Configuration, Configuration](c, fmt.Sprintf("/workspace/%s/configurations/%s", workspaceID, url.PathEscape(id)), body)
}

func (c *Client) DeleteConfiguration(workspaceID, id string) error {
	return deleteJSON(c, fmt.Sprintf("/workspace/%s/configurations/%s", workspaceID, url.PathEscape(id)))
}

func (c *Client) CreatePipeline(workspaceID string, body Pipeline) (*Pipeline, error) {
	return postJSON[Pipeline, Pipeline](c, fmt.Sprintf("/workspace/%s/pipelines/new", workspaceID), body)
}

func (c *Client) UpdatePipeline(workspaceID, id string, body Pipeline) (*Pipeline, error) {
	return putJSON[Pipeline, Pipeline](c, fmt.Sprintf("/workspace/%s/pipelines/%s", workspaceID, url.PathEscape(id)), body)
}

func (c *Client) DeletePipeline(workspaceID, id string) error {
	return deleteJSON(c, fmt.Sprintf("/workspace/%s/pipelines/%s", workspaceID, url.PathEscape(id)))
}

func (c *Client) GetManifest(workspaceID, componentType string) (*Manifest, error) {
	var manifest Manifest
	if err := c.GetJSON(fmt.Sprintf("/workspace/%s/cnips-manifests/%s", workspaceID, url.PathEscape(componentType)), &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (c *Client) UpsertManifest(workspaceID, componentType string, body Manifest) (*Manifest, error) {
	return putJSON[Manifest, Manifest](c, fmt.Sprintf("/workspace/%s/cnips-manifests/%s", workspaceID, url.PathEscape(componentType)), body)
}

func (c *Client) SubmitPublish(body PublishRequest) (*PublishResponse, error) {
	resp, err := postJSON[PublishRequest, PublishResponse](c, "/marketplace/publish", body)
	if resp != nil && resp.ID == "" {
		resp.ID = resp.AltID
	}
	return resp, err
}

func (c *Client) GetPublishStatus(id string) (*PublishStatus, error) {
	var status PublishStatus
	if err := c.GetJSON(fmt.Sprintf("/marketplace/publish/status/%s", url.PathEscape(id)), &status); err != nil {
		return nil, err
	}
	return &status, nil
}

func (c *Client) CancelPublishRequest(id string) error {
	return deleteJSON(c, fmt.Sprintf("/marketplace/publish/requests/%s", url.PathEscape(id)))
}

func (c *Client) Ping() error {
	resp, err := c.doGet("/ping")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %d for GET /ping: %s", c.baseURL, resp.StatusCode, truncate(string(body), 200))
	}
	return nil
}

// -----------------------------------------------------------------------
// internal
// -----------------------------------------------------------------------

// listAll pages through a list endpoint until all records are retrieved.
func listAll[T any](c *Client, path string) ([]T, error) {
	first, total, err := getListPageNumber[T](c, path, 0)
	if err != nil {
		return nil, err
	}
	if total <= 0 {
		return listAllWithoutTotal(c, path, first)
	}
	if len(first) == 0 || len(first) < defaultPageSize || total <= len(first) {
		return first, nil
	}
	pageCount := (total + defaultPageSize - 1) / defaultPageSize
	pages := make([][]T, pageCount)
	pages[0] = first

	const workers = 16
	jobs := make(chan int)
	errCh := make(chan error, pageCount-1)
	var wg sync.WaitGroup
	workerCount := workers
	if pageCount-1 < workerCount {
		workerCount = pageCount - 1
	}
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for page := range jobs {
				items, _, err := getListPageNumber[T](c, path, page)
				if err != nil {
					errCh <- err
					continue
				}
				pages[page] = items
			}
		}()
	}
	for page := 1; page < pageCount; page++ {
		jobs <- page
	}
	close(jobs)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return nil, err
		}
	}

	all := make([]T, 0, total)
	for _, items := range pages {
		all = append(all, items...)
	}
	if len(all) > total {
		all = all[:total]
	}
	return all, nil
}

func listAllWithoutTotal[T any](c *Client, path string, first []T) ([]T, error) {
	all := append([]T{}, first...)
	if len(first) == 0 || len(first) < defaultPageSize {
		return all, nil
	}
	for page := 1; ; page++ {
		items, _, err := getListPageNumber[T](c, path, page)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) == 0 || len(items) < defaultPageSize {
			return all, nil
		}
	}
}

func getListPageNumber[T any](c *Client, path string, page int) ([]T, int, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("size", strconv.Itoa(defaultPageSize))
	return getListPage[T](c, path+"?"+q.Encode())
}

func listAllFallback[T any](c *Client, paths ...string) ([]T, error) {
	var lastErr error
	for _, path := range paths {
		items, err := listAll[T](c, path)
		if err == nil {
			return items, nil
		}
		lastErr = err
		if !isNotFoundError(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "returned 404")
}

// getListPage fetches one page and returns (items, totalCount, error).
func getListPage[T any](c *Client, fullPath string) ([]T, int, error) {
	resp, err := c.doGet(fullPath)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("reading body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("%s returned %d for GET %s: %s", c.baseURL, resp.StatusCode, fullPath, truncate(string(body), 200))
	}

	var envelope APIResponse[ListData[T]]
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, 0, fmt.Errorf("decoding list response: %w\nbody: %s", err, truncate(string(body), 300))
	}
	if envelope.Data == nil {
		return nil, 0, nil
	}
	normalizeListIDs(envelope.Data.List)
	return envelope.Data.List, envelope.Data.Count, nil
}

func normalizeListIDs[T any](items []T) {
	for i := range items {
		switch v := any(&items[i]).(type) {
		case *Pipeline:
			if v.ID == "" {
				v.ID = v.AltID
			}
		case *Source:
			if v.ID == "" {
				v.ID = v.AltID
			}
		case *Transformation:
			if v.ID == "" {
				v.ID = v.AltID
			}
		case *Destination:
			if v.ID == "" {
				v.ID = v.AltID
			}
		case *Function:
			if v.ID == "" {
				v.ID = v.AltID
			}
		case *GlobalVariable:
			if v.ID == "" {
				v.ID = v.AltID
			}
		case *Configuration:
			if v.ID == "" {
				v.ID = v.AltID
			}
		case *App:
			if v.ID == "" {
				v.ID = v.AltID
			}
		case *Version:
			if v.ID == "" {
				v.ID = v.AltID
			}
		case *AppVersion:
			if v.ID == "" {
				v.ID = v.AltID
			}
		}
	}
}

// doGet issues an authenticated GET request against c.baseURL+path.
func (c *Client) doGet(path string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	if c.tenantKey != "" {
		req.Header.Set("x-tenant-key", c.tenantKey)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.httpClient.Do(req)
}

func (c *Client) doJSON(method, path string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding request body: %w", err)
	}
	req, err := http.NewRequest(method, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	if c.tenantKey != "" {
		req.Header.Set("x-tenant-key", c.tenantKey)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.httpClient.Do(req)
}

func postJSON[Req any, Resp any](c *Client, path string, body Req) (*Resp, error) {
	return writeJSON[Req, Resp](c, http.MethodPost, path, body)
}

func putJSON[Req any, Resp any](c *Client, path string, body Req) (*Resp, error) {
	return writeJSON[Req, Resp](c, http.MethodPut, path, body)
}

func writeJSON[Req any, Resp any](c *Client, method, path string, body Req) (*Resp, error) {
	resp, err := c.doJSON(method, path, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned %d for %s %s: %s", c.baseURL, resp.StatusCode, method, path, truncate(string(data), 300))
	}
	var envelope APIResponse[json.RawMessage]
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decoding response envelope: %w\nbody: %s", err, truncate(string(data), 300))
	}
	if envelope.Data == nil {
		return nil, nil
	}
	var out Resp
	if err := json.Unmarshal(*envelope.Data, &out); err != nil {
		return nil, fmt.Errorf("decoding response data: %w", err)
	}
	normalizeWriteID(&out)
	return &out, nil
}

func deleteJSON(c *Client, path string) error {
	req, err := http.NewRequest(http.MethodDelete, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("x-tenant-key", c.tenantKey)
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %d for DELETE %s: %s", c.baseURL, resp.StatusCode, path, truncate(string(body), 300))
	}
	return nil
}

func normalizeWriteID[T any](item *T) {
	switch v := any(item).(type) {
	case *Pipeline:
		if v.ID == "" {
			v.ID = v.AltID
		}
	case *Source:
		if v.ID == "" {
			v.ID = v.AltID
		}
	case *Transformation:
		if v.ID == "" {
			v.ID = v.AltID
		}
	case *Destination:
		if v.ID == "" {
			v.ID = v.AltID
		}
	case *Function:
		if v.ID == "" {
			v.ID = v.AltID
		}
	case *GlobalVariable:
		if v.ID == "" {
			v.ID = v.AltID
		}
	case *Configuration:
		if v.ID == "" {
			v.ID = v.AltID
		}
	case *App:
		if v.ID == "" {
			v.ID = v.AltID
		}
	case *AppVersion:
		if v.ID == "" {
			v.ID = v.AltID
		}
	}
}

// GetJSON fetches a non-paginated mgmt-srv endpoint and decodes the API envelope.
func (c *Client) GetJSON(path string, target any) error {
	resp, err := c.doGet(path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %d for GET %s: %s", c.baseURL, resp.StatusCode, path, truncate(string(body), 200))
	}
	var envelope APIResponse[json.RawMessage]
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decoding response envelope: %w\nbody: %s", err, truncate(string(body), 300))
	}
	if envelope.Data == nil {
		return nil
	}
	if err := json.Unmarshal(*envelope.Data, target); err != nil {
		return fmt.Errorf("decoding response data: %w", err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
