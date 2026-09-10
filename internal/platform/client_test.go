package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
)

func TestListAllUsesMgmtSrvPaginationUntilTotalCount(t *testing.T) {
	type item struct {
		ID string `json:"_id"`
	}

	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.RawQuery)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		size, _ := strconv.Atoi(r.URL.Query().Get("size"))
		if size != defaultPageSize {
			t.Fatalf("size query = %d, want %d", size, defaultPageSize)
		}

		start := page * size
		total := 43
		end := start + size
		if end > total {
			end = total
		}
		items := make([]item, 0, end-start)
		for i := start; i < end; i++ {
			items = append(items, item{ID: strconv.Itoa(i)})
		}

		_ = json.NewEncoder(w).Encode(APIResponse[ListData[item]]{
			Success: true,
			Status:  http.StatusOK,
			Data: &ListData[item]{
				List:  items,
				Count: total,
			},
		})
	}))
	defer server.Close()

	got, err := listAll[item](NewClient(server.URL, "", ""), "/items")
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if len(got) != 43 {
		t.Fatalf("len(got) = %d, want 43", len(got))
	}
	wantQueries := []string{"page=0&size=20", "page=1&size=20", "page=2&size=20"}
	if len(requested) != len(wantQueries) {
		t.Fatalf("requested %d pages %v, want %d", len(requested), requested, len(wantQueries))
	}
	slices.Sort(requested)
	for i := range wantQueries {
		if requested[i] != wantQueries[i] {
			t.Fatalf("request %d query = %q, want %q", i, requested[i], wantQueries[i])
		}
	}
}

func TestClientOmitsEmptyTenantKeyHeader(t *testing.T) {
	var sawTenantHeader bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawTenantHeader = r.Header["X-Tenant-Key"]
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"status":200}`))
	}))
	defer server.Close()

	resp, err := NewClient(server.URL, "", "").doGet("/ping")
	if err != nil {
		t.Fatalf("doGet: %v", err)
	}
	_ = resp.Body.Close()

	if sawTenantHeader {
		t.Fatal("x-tenant-key header was sent for empty tenant key")
	}
}

func TestClientSendsExplicitTenantKeyHeader(t *testing.T) {
	var sawTenant string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawTenant = r.Header.Get("x-tenant-key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"status":200}`))
	}))
	defer server.Close()

	resp, err := NewClient(server.URL, "cnips-dev", "").doGet("/ping")
	if err != nil {
		t.Fatalf("doGet: %v", err)
	}
	_ = resp.Body.Close()

	if sawTenant != "cnips-dev" {
		t.Fatalf("tenant header = %q, want cnips-dev", sawTenant)
	}
}

func TestListAllContinuesWhenTotalCountIsMissing(t *testing.T) {
	type item struct {
		ID string `json:"_id"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		count := defaultPageSize
		if page == 2 {
			count = 3
		}
		items := make([]item, 0, count)
		for i := 0; i < count; i++ {
			items = append(items, item{ID: strconv.Itoa(page*defaultPageSize + i)})
		}

		_ = json.NewEncoder(w).Encode(APIResponse[ListData[item]]{
			Success: true,
			Status:  http.StatusOK,
			Data: &ListData[item]{
				List: items,
			},
		})
	}))
	defer server.Close()

	got, err := listAll[item](NewClient(server.URL, "", ""), "/items")
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if len(got) != 43 {
		t.Fatalf("len(got) = %d, want 43", len(got))
	}
}

func TestListAllNormalizesIDField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"status":  http.StatusOK,
			"data": map[string]any{
				"count": 1,
				"list": []map[string]any{
					{"id": "tx-1", "name": "Transform"},
				},
			},
		})
	}))
	defer server.Close()

	got, err := listAll[Transformation](NewClient(server.URL, "", ""), "/transformations")
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].ID != "tx-1" {
		t.Fatalf("ID = %q, want tx-1", got[0].ID)
	}
}

func TestListAllPrefersStringIDOverMongoID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"status":  http.StatusOK,
			"data": map[string]any{
				"count": 1,
				"list": []map[string]any{
					{"id": "uuid-transform-id", "_id": "6a856acead9e4a260db134bb", "name": "Transform"},
				},
			},
		})
	}))
	defer server.Close()

	got, err := listAll[Transformation](NewClient(server.URL, "", ""), "/transformations")
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if got[0].ID != "uuid-transform-id" {
		t.Fatalf("ID = %q, want uuid-transform-id", got[0].ID)
	}
	if got[0].AltID != "6a856acead9e4a260db134bb" {
		t.Fatalf("AltID = %q, want mongo id fallback", got[0].AltID)
	}
}

func TestListGlobalVariablesUsesMgmtSrvRouteBeforeLegacyVariablesRoute(t *testing.T) {
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		switch r.URL.Path {
		case "/workspace/default/globalvariable":
			_ = json.NewEncoder(w).Encode(APIResponse[ListData[GlobalVariable]]{
				Success: true,
				Status:  http.StatusOK,
				Data: &ListData[GlobalVariable]{
					List:  []GlobalVariable{{AltID: "secret-token", Key: "Secret Token"}},
					Count: 1,
				},
			})
		case "/workspace/default/variables":
			t.Fatalf("legacy variables route should not be requested when globalvariable succeeds")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	got, err := NewClient(server.URL, "", "").ListGlobalVariables("default")
	if err != nil {
		t.Fatalf("ListGlobalVariables: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].ID != "secret-token" {
		t.Fatalf("ID = %q, want secret-token", got[0].ID)
	}
	if len(requested) != 1 || requested[0] != "/workspace/default/globalvariable" {
		t.Fatalf("requested paths = %v, want only /workspace/default/globalvariable", requested)
	}
}

func TestListGlobalVariablesFallsBackToLegacyVariablesRouteOnNotFound(t *testing.T) {
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		switch r.URL.Path {
		case "/workspace/default/globalvariable":
			http.NotFound(w, r)
		case "/workspace/default/variables":
			_ = json.NewEncoder(w).Encode(APIResponse[ListData[GlobalVariable]]{
				Success: true,
				Status:  http.StatusOK,
				Data: &ListData[GlobalVariable]{
					List:  []GlobalVariable{{AltID: "legacy-token", Key: "Legacy Token"}},
					Count: 1,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	got, err := NewClient(server.URL, "", "").ListGlobalVariables("default")
	if err != nil {
		t.Fatalf("ListGlobalVariables: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].ID != "legacy-token" {
		t.Fatalf("ID = %q, want legacy-token", got[0].ID)
	}
	want := []string{"/workspace/default/globalvariable", "/workspace/default/variables"}
	if len(requested) != len(want) {
		t.Fatalf("requested paths = %v, want %v", requested, want)
	}
	for i := range want {
		if requested[i] != want[i] {
			t.Fatalf("requested[%d] = %q, want %q", i, requested[i], want[i])
		}
	}
}

func TestManifestClientUsesWorkspaceScopedComponentTypeRoute(t *testing.T) {
	var seenGet, seenPut bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/workspace/default/cnips-manifests/transformations":
			seenGet = true
			_ = json.NewEncoder(w).Encode(APIResponse[Manifest]{
				Success: true,
				Status:  http.StatusOK,
				Data: &Manifest{
					WorkspaceID:   "default",
					ComponentType: "transformations",
					Files: map[string]ManifestFile{
						"transformations/a/component.yaml": {Digest: "abc"},
					},
				},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/workspace/default/cnips-manifests/transformations":
			seenPut = true
			_ = json.NewEncoder(w).Encode(APIResponse[Manifest]{
				Success: true,
				Status:  http.StatusOK,
				Data:    &Manifest{WorkspaceID: "default", ComponentType: "transformations"},
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	manifest, err := client.GetManifest("default", "transformations")
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if manifest.Files["transformations/a/component.yaml"].Digest != "abc" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	if _, err := client.UpsertManifest("default", "transformations", *manifest); err != nil {
		t.Fatalf("UpsertManifest: %v", err)
	}
	if !seenGet || !seenPut {
		t.Fatalf("seenGet=%v seenPut=%v", seenGet, seenPut)
	}
}
