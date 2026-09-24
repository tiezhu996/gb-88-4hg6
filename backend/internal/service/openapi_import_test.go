package service

import (
	"testing"

	"github.com/mockhub/mockhub/internal/dto"
	"github.com/mockhub/mockhub/internal/model"
	"github.com/mockhub/mockhub/internal/repository"
)

// docWith builds an OpenAPI 3 document for the given path/method matrix.
func docWith(entries map[string]map[string]any) map[string]any {
	paths := map[string]any{}
	for path, methods := range entries {
		paths[path] = methods
	}
	return map[string]any{"openapi": "3.0.0", "paths": paths}
}

func jsonExample(status int, example map[string]any) map[string]any {
	return map[string]any{
		"responses": map[string]any{
			statusString(status): map[string]any{
				"content": map[string]any{"application/json": map[string]any{"example": example}},
			},
		},
	}
}

func statusString(status int) string {
	switch status {
	case 200:
		return "200"
	case 201:
		return "201"
	default:
		return "200"
	}
}

func TestPreviewOpenAPIClassifiesEntries(t *testing.T) {
	db := newTestDB(t)
	projects := repository.NewProjectRepository(db)
	endpoints := repository.NewEndpointRepository(db)
	logs := repository.NewRequestLogRepository(db)
	svc := NewEndpointService(projects, endpoints, discardLogger())
	dev := createUser(t, db, "dev", RoleDev)
	project, _ := NewProjectService(projects, discardLogger()).Create("p", "d", dev.ID, "http://localhost:3119")

	// Seed an existing endpoint: GET /api/users.
	existing, err := svc.Create(project.ID, dev.ID, RoleDev, dto.EndpointRequest{
		Path: "/api/users", Method: "GET", StatusCode: 200, ResponseBody: `{"old":true}`,
	})
	if err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}

	doc := docWith(map[string]map[string]any{
		"/api/users": {
			"get": jsonExample(200, map[string]any{"fresh": "data"}),
			"post": map[string]any{ // valid new entry without an example body
				"responses": map[string]any{"201": map[string]any{"description": "created"}},
			},
			"weird": map[string]any{"responses": map[string]any{}},
		},
		"api/no-leading-slash": {
			"get": jsonExample(200, map[string]any{}),
		},
	})
	// Operation definitions that are not objects.
	doc["paths"].(map[string]any)["/broken"] = "not-an-object"

	preview, err := svc.PreviewOpenAPI(project.ID, dev.ID, RoleDev, doc)
	if err != nil {
		t.Fatalf("PreviewOpenAPI: %v", err)
	}
	if len(preview.New) != 1 || preview.New[0].Method != "POST" || preview.New[0].Path != "/api/users" {
		t.Fatalf("new = %+v, want single POST /api/users", preview.New)
	}
	if preview.New[0].ResponseBody != `{"code":0,"message":"ok"}` {
		t.Fatalf("new entry default body = %q", preview.New[0].ResponseBody)
	}
	if len(preview.Duplicate) != 1 {
		t.Fatalf("duplicate = %+v, want 1", preview.Duplicate)
	}
	dup := preview.Duplicate[0]
	if dup.Method != "GET" || dup.Path != "/api/users" || dup.ExistingID != existing.ID {
		t.Fatalf("duplicate = %+v", dup)
	}
	if dup.ResponseBody != `{"fresh":"data"}` {
		t.Fatalf("duplicate replacement body = %q", dup.ResponseBody)
	}
	reasons := map[string]bool{}
	for _, f := range preview.Invalid {
		reasons[f.Method+"|"+f.Path] = true
	}
	if !reasons["weird|/api/users"] {
		t.Fatalf("invalid should include unsupported method, got %+v", preview.Invalid)
	}
	if !reasons["GET|api/no-leading-slash"] {
		t.Fatalf("invalid should include bad path, got %+v", preview.Invalid)
	}
	if !reasons["|/broken"] {
		t.Fatalf("invalid should include non-object path item, got %+v", preview.Invalid)
	}

	// Preview must not have written anything.
	apis, _ := svc.List(project.ID, dev.ID, RoleDev)
	if len(apis) != 1 {
		t.Fatalf("preview wrote data: %d endpoints", len(apis))
	}
	_ = logs // keep repository import meaningful for later subtests wiring
}

func TestPreviewOpenAPIRejectsDocumentWithoutPaths(t *testing.T) {
	db := newTestDB(t)
	projects := repository.NewProjectRepository(db)
	endpoints := repository.NewEndpointRepository(db)
	svc := NewEndpointService(projects, endpoints, discardLogger())
	dev := createUser(t, db, "dev", RoleDev)
	project, _ := NewProjectService(projects, discardLogger()).Create("p", "d", dev.ID, "http://localhost:3119")

	if _, err := svc.PreviewOpenAPI(project.ID, dev.ID, RoleDev, map[string]any{"openapi": "3.0.0"}); err == nil {
		t.Fatal("expected error for document without paths")
	}
}

func TestPreviewOpenAPINormalizesBracesPathParams(t *testing.T) {
	db := newTestDB(t)
	projects := repository.NewProjectRepository(db)
	endpoints := repository.NewEndpointRepository(db)
	svc := NewEndpointService(projects, endpoints, discardLogger())
	dev := createUser(t, db, "dev", RoleDev)
	project, _ := NewProjectService(projects, discardLogger()).Create("p", "d", dev.ID, "http://localhost:3119")

	doc := docWith(map[string]map[string]any{
		"/api/users/{id}": {"get": jsonExample(200, map[string]any{"id": 7})},
	})
	preview, err := svc.PreviewOpenAPI(project.ID, dev.ID, RoleDev, doc)
	if err != nil {
		t.Fatalf("PreviewOpenAPI: %v", err)
	}
	if len(preview.New) != 1 || preview.New[0].Path != "/api/users/:id" {
		t.Fatalf("expected normalized :id path, got %+v", preview.New)
	}
}

func TestCommitOpenAPICreateSkipReplace(t *testing.T) {
	db := newTestDB(t)
	projects := repository.NewProjectRepository(db)
	endpoints := repository.NewEndpointRepository(db)
	logs := repository.NewRequestLogRepository(db)
	svc := NewEndpointService(projects, endpoints, discardLogger())
	dev := createUser(t, db, "dev", RoleDev)
	project, _ := NewProjectService(projects, discardLogger()).Create("p", "d", dev.ID, "http://localhost:3119")

	// Existing GET /api/users with a historical request log.
	existing, err := svc.Create(project.ID, dev.ID, RoleDev, dto.EndpointRequest{
		Path: "/api/users", Method: "GET", StatusCode: 200,
		ResponseBody: `{"old":true}`, Delay: 100,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	oldLog := &model.RequestLog{ProjectID: project.ID, APIID: existing.ID, Method: "GET", Path: "/api/users", ResponseStatus: 200}
	if err := logs.Create(oldLog); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	doc := docWith(map[string]map[string]any{
		"/api/users": {
			"get":  jsonExample(200, map[string]any{"v": 2}),
			"post": jsonExample(201, map[string]any{"id": 1}),
		},
	})
	selections := []dto.SwaggerImportSelection{
		{Method: "GET", Path: "/api/users", Action: dto.ImportActionReplace},
		{Method: "POST", Path: "/api/users", Action: dto.ImportActionCreate},
	}
	result, err := svc.CommitOpenAPI(project.ID, dev.ID, RoleDev, doc, selections)
	if err != nil {
		t.Fatalf("CommitOpenAPI: %v", err)
	}
	if result.Created != 1 || result.Replaced != 1 || len(result.Failed) != 0 {
		t.Fatalf("result = %+v", result)
	}

	// Replacement must keep the same row (and thus log association).
	updated, err := endpoints.FindByID(existing.ID)
	if err != nil {
		t.Fatalf("find replaced endpoint: %v", err)
	}
	if updated.ResponseBody != `{"v":2}` {
		t.Fatalf("replaced body = %q", updated.ResponseBody)
	}
	if updated.Delay != 100 {
		t.Fatalf("replacement should preserve untouched fields, delay = %d", updated.Delay)
	}
	var linked model.RequestLog
	if err := db.First(&linked, oldLog.ID).Error; err != nil {
		t.Fatalf("load log: %v", err)
	}
	if linked.APIID != existing.ID {
		t.Fatalf("log apiId = %d, want %d (logs must stay linked after replace)", linked.APIID, existing.ID)
	}

	// Skip action leaves duplicates untouched.
	doc2 := docWith(map[string]map[string]any{
		"/api/users": {"get": jsonExample(200, map[string]any{"v": 3})},
	})
	res2, err := svc.CommitOpenAPI(project.ID, dev.ID, RoleDev, doc2, []dto.SwaggerImportSelection{
		{Method: "GET", Path: "/api/users", Action: dto.ImportActionSkip},
	})
	if err != nil {
		t.Fatalf("CommitOpenAPI skip: %v", err)
	}
	if res2.Skipped != 1 || res2.Replaced != 0 {
		t.Fatalf("skip result = %+v", res2)
	}
	again, _ := endpoints.FindByID(existing.ID)
	if again.ResponseBody != `{"v":2}` {
		t.Fatalf("skip changed body: %q", again.ResponseBody)
	}
}

func TestCommitOpenAPIRejectsActionMismatches(t *testing.T) {
	db := newTestDB(t)
	projects := repository.NewProjectRepository(db)
	endpoints := repository.NewEndpointRepository(db)
	svc := NewEndpointService(projects, endpoints, discardLogger())
	dev := createUser(t, db, "dev", RoleDev)
	project, _ := NewProjectService(projects, discardLogger()).Create("p", "d", dev.ID, "http://localhost:3119")

	// GET exists, POST does not.
	if _, err := svc.Create(project.ID, dev.ID, RoleDev, dto.EndpointRequest{
		Path: "/api/users", Method: "GET", StatusCode: 200, ResponseBody: `{}`,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	doc := docWith(map[string]map[string]any{
		"/api/users": {
			"get":  jsonExample(200, map[string]any{"v": 1}),
			"post": jsonExample(201, map[string]any{"id": 1}),
		},
	})
	selections := []dto.SwaggerImportSelection{
		{Method: "GET", Path: "/api/users", Action: dto.ImportActionCreate},  // already exists
		{Method: "POST", Path: "/api/users", Action: dto.ImportActionReplace}, // missing
	}
	result, err := svc.CommitOpenAPI(project.ID, dev.ID, RoleDev, doc, selections)
	if err != nil {
		t.Fatalf("CommitOpenAPI: %v", err)
	}
	if len(result.Failed) != 2 || result.Created != 0 || result.Replaced != 0 {
		t.Fatalf("expected 2 per-entry failures, got %+v", result)
	}
	for _, f := range result.Failed {
		if f.Reason == "" {
			t.Fatalf("failure must explain the entry: %+v", f)
		}
	}
}

func TestImportOpenAPILegacySkipsDuplicates(t *testing.T) {
	db := newTestDB(t)
	projects := repository.NewProjectRepository(db)
	endpoints := repository.NewEndpointRepository(db)
	svc := NewEndpointService(projects, endpoints, discardLogger())
	dev := createUser(t, db, "dev", RoleDev)
	project, _ := NewProjectService(projects, discardLogger()).Create("p", "d", dev.ID, "http://localhost:3119")

	if _, err := svc.Create(project.ID, dev.ID, RoleDev, dto.EndpointRequest{
		Path: "/api/orders", Method: "GET", StatusCode: 200, ResponseBody: `{}`,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	doc := docWith(map[string]map[string]any{
		"/api/orders": {
			"get":  jsonExample(200, map[string]any{}),
			"post": jsonExample(201, map[string]any{"id": 1}),
		},
	})
	created, err := svc.ImportOpenAPI(project.ID, dev.ID, RoleDev, doc)
	if err != nil {
		t.Fatalf("ImportOpenAPI: %v", err)
	}
	if created != 1 {
		t.Fatalf("legacy import created = %d, want 1 (duplicate skipped)", created)
	}
}
