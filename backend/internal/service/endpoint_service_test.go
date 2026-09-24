package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/mockhub/mockhub/internal/constants"
	"github.com/mockhub/mockhub/internal/dto"
	"github.com/mockhub/mockhub/internal/model"
	"github.com/mockhub/mockhub/internal/repository"
)

func TestEndpointServiceCRUDAndAccess(t *testing.T) {
	db := newTestDB(t)
	projects := repository.NewProjectRepository(db)
	endpoints := repository.NewEndpointRepository(db)
	svc := NewEndpointService(projects, endpoints, discardLogger())

	dev := createUser(t, db, "dev", RoleDev)
	other := createUser(t, db, "other", RoleDev)
	lead := createUser(t, db, "lead", RoleLead)
	project, err := NewProjectService(projects, discardLogger()).Create("p", "d", dev.ID, "http://localhost:3119")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	req := dto.EndpointRequest{
		Path:            "/api/users/:id",
		Method:          "GET",
		StatusCode:      200,
		ResponseBody:    `{"ok":true}`,
		ResponseHeaders: map[string]string{"X-Demo": "1"},
	}
	created, err := svc.Create(project.ID, dev.ID, RoleDev, req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 || created.ResponseHeaders["X-Demo"] != "1" {
		t.Fatalf("Create = %+v", created)
	}

	t.Run("owner list", func(t *testing.T) {
		apis, err := svc.List(project.ID, dev.ID, RoleDev)
		if err != nil || len(apis) != 1 {
			t.Fatalf("List = %d, %v; want 1 api", len(apis), err)
		}
	})

	t.Run("lead list", func(t *testing.T) {
		apis, err := svc.List(project.ID, lead.ID, RoleLead)
		if err != nil || len(apis) != 1 {
			t.Fatalf("List lead = %d, %v; want 1 api", len(apis), err)
		}
	})

	t.Run("other dev forbidden", func(t *testing.T) {
		_, err := svc.List(project.ID, other.ID, RoleDev)
		if !errors.Is(err, constants.ErrForbidden) {
			t.Fatalf("List err = %v, want ErrForbidden", err)
		}
	})

	t.Run("update", func(t *testing.T) {
		updated, err := svc.Update(project.ID, created.ID, dev.ID, RoleDev, dto.EndpointRequest{
			Path: "/api/users/:id", Method: "PUT", StatusCode: 200, ResponseBody: `{"updated":true}`,
		})
		if err != nil || updated.Method != "PUT" {
			t.Fatalf("Update = %+v, %v", updated, err)
		}
	})

	t.Run("delete then missing", func(t *testing.T) {
		if err := svc.Delete(project.ID, created.ID, dev.ID, RoleDev); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := svc.Get(project.ID, created.ID, dev.ID, RoleDev); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("Get after delete err = %v, want ErrNotFound", err)
		}
	})
}

// newImportProject builds an isolated project owned by a fresh user for
// import tests and returns the project id, an endpoint repo and the owner.
func newImportProject(t *testing.T, db *gorm.DB) (uint, *repository.EndpointRepository, *model.User) {
	t.Helper()
	projects := repository.NewProjectRepository(db)
	owner := createUser(t, db, fmt.Sprintf("owner_%d", time.Now().UnixNano()), RoleDev)
	project, err := NewProjectService(projects, discardLogger()).Create("p", "d", owner.ID, "http://localhost:3119")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project.ID, repository.NewEndpointRepository(db), owner
}

func sampleDoc() map[string]any {
	paths := map[string]any{
		"/api/orders": map[string]any{
			"get": map[string]any{
				"responses": map[string]any{
					"200": map[string]any{"content": map[string]any{"application/json": map[string]any{"example": map[string]any{"code": 0}}}},
				},
			},
			"post": map[string]any{
				"responses": map[string]any{"201": map[string]any{"content": map[string]any{"application/json": map[string]any{"example": map[string]any{"id": 1}}}}},
			},
		},
		"/api/products": map[string]any{
			"trace": map[string]any{}, // unsupported method -> invalid
		},
		"bad-path": map[string]any{ // path must start with /
			"get": map[string]any{},
		},
	}
	return map[string]any{"openapi": "3.0.0", "paths": paths}
}

func TestPreviewOpenAPIClassification(t *testing.T) {
	db := newTestDB(t)
	projects := repository.NewProjectRepository(db)
	endpoints := repository.NewEndpointRepository(db)
	svc := NewEndpointService(projects, endpoints, discardLogger())
	projectID, _, dev := newImportProject(t, db)

	// Seed an existing GET /api/orders endpoint.
	if err := endpoints.Create(&model.MockAPI{
		ProjectID: projectID, Path: "/api/orders", Method: "GET", StatusCode: 200, ResponseBody: `{"old":true}`,
	}); err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}

	preview, err := svc.PreviewOpenAPI(projectID, dev.ID, RoleDev, sampleDoc())
	if err != nil {
		t.Fatalf("PreviewOpenAPI: %v", err)
	}
	if preview.NewCount != 1 || preview.DupCount != 1 || preview.InvalidCount != 2 {
		t.Fatalf("counts = +%d dup%d invalid%d, want 1/1/2", preview.NewCount, preview.DupCount, preview.InvalidCount)
	}
	if len(preview.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(preview.Entries))
	}
	byKey := map[string]dto.SwaggerPreviewEntry{}
	for _, e := range preview.Entries {
		byKey[e.Method+" "+e.Path] = e
	}
	dup, ok := byKey["GET /api/orders"]
	if !ok || dup.Status != StatusDuplicate || dup.ExistingID == 0 {
		t.Fatalf("GET /api/orders = %+v, want duplicate with existing id", dup)
	}
	if dup.StatusCode != 200 || dup.ResponseBody != `{"code":0}` {
		t.Fatalf("duplicate entry content = %+v", dup)
	}
	newEntry, ok := byKey["POST /api/orders"]
	if !ok || newEntry.Status != StatusNew || newEntry.StatusCode != 201 || newEntry.ResponseBody != `{"id":1}` {
		t.Fatalf("POST /api/orders = %+v, want new entry status 201", newEntry)
	}
	if len(preview.Invalid) != 2 {
		t.Fatalf("invalid = %+v", preview.Invalid)
	}
}

func TestPreviewOpenAPIMissingPaths(t *testing.T) {
	db := newTestDB(t)
	projects := repository.NewProjectRepository(db)
	endpoints := repository.NewEndpointRepository(db)
	svc := NewEndpointService(projects, endpoints, discardLogger())
	projectID, _, dev := newImportProject(t, db)

	if _, err := svc.PreviewOpenAPI(projectID, dev.ID, RoleDev, map[string]any{"openapi": "3.0.0"}); err == nil {
		t.Fatal("missing paths should return an error")
	}
}

func commitDoc() map[string]any {
	paths := map[string]any{
		"/api/orders": map[string]any{
			"get": map[string]any{
				"responses": map[string]any{
					"200": map[string]any{"content": map[string]any{"application/json": map[string]any{"example": map[string]any{"code": 0}}}},
				},
			},
		},
		"/api/users": map[string]any{
			"post": map[string]any{
				"responses": map[string]any{
					"201": map[string]any{"content": map[string]any{"application/json": map[string]any{"example": map[string]any{"id": 9}}}},
				},
			},
		},
		"/api/items": map[string]any{
			"delete": map[string]any{
				"responses": map[string]any{"204": map[string]any{}},
			},
		},
	}
	return map[string]any{"openapi": "3.0.0", "paths": paths}
}

func TestCommitOpenAPICreateReplaceSkipAndLogs(t *testing.T) {
	db := newTestDB(t)
	projects := repository.NewProjectRepository(db)
	endpoints := repository.NewEndpointRepository(db)
	logs := repository.NewRequestLogRepository(db)
	svc := NewEndpointService(projects, endpoints, discardLogger())
	projectID, _, dev := newImportProject(t, db)

	// Existing endpoint that will be replaced, plus a historical log linked to it.
	existing := &model.MockAPI{
		ProjectID: projectID, Path: "/api/orders", Method: "GET", StatusCode: 500,
		ResponseBody: `{"old":true}`, ResponseHeaders: map[string]string{"X-Old": "1"},
	}
	if err := endpoints.Create(existing); err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}
	oldLog := &model.RequestLog{ProjectID: projectID, APIID: existing.ID, Method: "GET", Path: "/api/orders", ResponseStatus: 500}
	if err := logs.Create(oldLog); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	result, err := svc.CommitOpenAPI(projectID, dev.ID, RoleDev, dto.SwaggerCommitRequest{
		Document: commitDoc(),
		Selections: []dto.SwaggerImportSelection{
			{Method: "GET", Path: "/api/orders", Action: ActionReplace},
			{Method: "POST", Path: "/api/users", Action: ActionCreate},
			{Method: "DELETE", Path: "/api/items", Action: ActionSkip},
		},
	})
	if err != nil {
		t.Fatalf("CommitOpenAPI: %v", err)
	}
	if result.Created != 1 || result.Replaced != 1 || result.Skipped != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}

	// Replacement kept the same row ID so the historical log stays linked.
	replaced, err := endpoints.FindByID(existing.ID)
	if err != nil {
		t.Fatalf("find replaced: %v", err)
	}
	if replaced.StatusCode != 200 || replaced.ResponseBody != `{"code":0}` {
		t.Fatalf("replaced content = %+v", replaced)
	}
	if len(replaced.ResponseHeaders) != 0 || replaced.Delay != 0 || len(replaced.Conditions) != 0 {
		t.Fatalf("replaced endpoint should reset headers/delay/conditions, got %+v", replaced)
	}
	var linked model.RequestLog
	if err := db.First(&linked, oldLog.ID).Error; err != nil {
		t.Fatalf("reload log: %v", err)
	}
	if linked.APIID != existing.ID {
		t.Fatalf("log apiId = %d, want %d (logs must stay linked after replace)", linked.APIID, existing.ID)
	}

	// One endpoint created, one replaced, skipped entry not present.
	all, _ := endpoints.ListByProject(projectID)
	if len(all) != 2 {
		t.Fatalf("endpoints after commit = %d, want 2", len(all))
	}
}

func TestCommitOpenAPIPerEntryFailuresKeepPreviewSafe(t *testing.T) {
	db := newTestDB(t)
	projects := repository.NewProjectRepository(db)
	endpoints := repository.NewEndpointRepository(db)
	svc := NewEndpointService(projects, endpoints, discardLogger())
	projectID, _, dev := newImportProject(t, db)

	// GET /api/orders already exists; POST /api/users is genuinely new.
	if err := endpoints.Create(&model.MockAPI{
		ProjectID: projectID, Path: "/api/orders", Method: "GET", StatusCode: 200,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	result, err := svc.CommitOpenAPI(projectID, dev.ID, RoleDev, dto.SwaggerCommitRequest{
		Document: commitDoc(),
		Selections: []dto.SwaggerImportSelection{
			{Method: "GET", Path: "/api/orders", Action: ActionCreate},   // conflicts with existing
			{Method: "POST", Path: "/api/users", Action: ActionReplace},  // nothing to replace
			{Method: "DELETE", Path: "/api/items", Action: ActionCreate}, // succeeds
			{Method: "GET", Path: "/api/ghost", Action: ActionCreate},    // not in document
		},
	})
	if err != nil {
		t.Fatalf("CommitOpenAPI: %v", err)
	}
	if result.Created != 1 || result.Replaced != 0 || result.Skipped != 0 || result.Failed != 3 {
		t.Fatalf("result = %+v, want 1 created and 3 failed", result)
	}
	reasons := map[string]string{}
	for _, f := range result.Failures {
		reasons[fmt.Sprintf("%s %s %s", f.Method, f.Path, f.Action)] = f.Reason
	}
	if reasons["GET /api/orders create"] == "" || reasons["POST /api/users replace"] == "" || reasons["GET /api/ghost create"] == "" {
		t.Fatalf("failure reasons missing: %+v", reasons)
	}

	// Successful entry was persisted despite sibling failures; the preview can
	// be kept open and the failed selections retried.
	all, _ := endpoints.ListByProject(projectID)
	if len(all) != 2 {
		t.Fatalf("endpoints = %d, want 2 (partial success)", len(all))
	}
}

func TestCommitOpenAPIIdempotentRetry(t *testing.T) {
	db := newTestDB(t)
	projects := repository.NewProjectRepository(db)
	endpoints := repository.NewEndpointRepository(db)
	svc := NewEndpointService(projects, endpoints, discardLogger())
	projectID, _, dev := newImportProject(t, db)

	doc := commitDoc()
	// Existing GET /api/orders already matches the document content, as if a
	// previous commit had replaced it before a sibling failed.
	if err := endpoints.Create(&model.MockAPI{
		ProjectID: projectID, Path: "/api/orders", Method: "GET", StatusCode: 200,
		ResponseBody: `{"code":0}`,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	result, err := svc.CommitOpenAPI(projectID, dev.ID, RoleDev, dto.SwaggerCommitRequest{
		Document: doc,
		Selections: []dto.SwaggerImportSelection{
			{Method: "GET", Path: "/api/orders", Action: ActionReplace}, // already up to date
		},
	})
	if err != nil {
		t.Fatalf("CommitOpenAPI: %v", err)
	}
	if result.Replaced != 1 || result.Failed != 0 {
		t.Fatalf("idempotent replace result = %+v", result)
	}

	// Replaying a create after it already applied with identical content is
	// also idempotent; drifted content must not be silently overwritten.
	first, err := svc.CommitOpenAPI(projectID, dev.ID, RoleDev, dto.SwaggerCommitRequest{
		Document: doc,
		Selections: []dto.SwaggerImportSelection{
			{Method: "POST", Path: "/api/users", Action: ActionCreate},
		},
	})
	if err != nil || first.Created != 1 {
		t.Fatalf("first create = %+v, %v", first, err)
	}
	replay, err := svc.CommitOpenAPI(projectID, dev.ID, RoleDev, dto.SwaggerCommitRequest{
		Document: doc,
		Selections: []dto.SwaggerImportSelection{
			{Method: "POST", Path: "/api/users", Action: ActionCreate},
		},
	})
	if err != nil {
		t.Fatalf("replay create: %v", err)
	}
	if replay.Created != 1 || replay.Failed != 0 {
		t.Fatalf("replay create result = %+v, want idempotent success", replay)
	}

	// Content drift on the existing row must surface as a conflict, not be
	// overwritten by a "create" replay.
	if _, err := svc.Create(projectID, dev.ID, RoleDev, dto.EndpointRequest{
		Path: "/api/items", Method: "DELETE", StatusCode: 204, ResponseBody: `{"drift":true}`,
	}); err != nil {
		t.Fatalf("seed drifted endpoint: %v", err)
	}
	drift, err := svc.CommitOpenAPI(projectID, dev.ID, RoleDev, dto.SwaggerCommitRequest{
		Document: doc,
		Selections: []dto.SwaggerImportSelection{
			{Method: "DELETE", Path: "/api/items", Action: ActionCreate},
		},
	})
	if err != nil {
		t.Fatalf("drift commit: %v", err)
	}
	if drift.Failed != 1 {
		t.Fatalf("drift result = %+v, want 1 conflict failure", drift)
	}
}
