package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mockhub/mockhub/internal/config"
	"github.com/mockhub/mockhub/internal/middleware"
	"github.com/mockhub/mockhub/internal/model"
	"github.com/mockhub/mockhub/internal/repository"
	"github.com/mockhub/mockhub/internal/service"
	"github.com/mockhub/mockhub/internal/util"
)

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func setupImportRouter(t *testing.T) (*gin.Engine, *repository.EndpointRepository, *repository.RequestLogRepository, uint, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := newHandlerTestDB(t)
	projectRepo := repository.NewProjectRepository(db)
	endpointRepo := repository.NewEndpointRepository(db)
	logRepo := repository.NewRequestLogRepository(db)
	logger := discardHandlerLogger()

	projectSvc := service.NewProjectService(projectRepo, logger)
	endpointSvc := service.NewEndpointService(projectRepo, endpointRepo, logger)
	h := NewEndpointHandler(endpointSvc, logger)

	cfg := &config.Config{JWTSecret: "test-secret-that-is-long-enough-for-tests"}
	dev := createHandlerUser(t, db, "dev", service.RoleDev)
	project, err := projectSvc.Create("p", "d", dev.ID, "http://localhost:3119")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	token, err := util.GenerateToken(cfg.JWTSecret, dev.ID, service.RoleDev, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	r := gin.New()
	g := r.Group("/api/v1")
	g.POST("/projects/:projectId/swagger/preview", middleware.JWTAuth(cfg, logger), h.PreviewSwagger)
	g.POST("/projects/:projectId/swagger/commit", middleware.JWTAuth(cfg, logger), h.CommitSwagger)
	return r, endpointRepo, logRepo, project.ID, token
}

func doJSON(t *testing.T, r *gin.Engine, token, method, path string, body any) (int, envelope) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	return w.Code, env
}

func TestSwaggerPreviewCommitHTTPEndToEnd(t *testing.T) {
	r, endpointRepo, logRepo, projectID, token := setupImportRouter(t)

	// Seed an existing GET /api/users and attach a historical log to it.
	existing := &model.MockAPI{
		ProjectID: projectID, Path: "/api/users", Method: "GET",
		StatusCode: 200, ResponseBody: `{"old":true}`,
	}
	if err := endpointRepo.Create(existing); err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}
	existingID := existing.ID
	if err := logRepo.Create(&model.RequestLog{
		ProjectID: projectID, APIID: existingID, Method: "GET", Path: "/api/users", ResponseStatus: 200,
	}); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	doc := map[string]any{
		"openapi": "3.0.0",
		"paths": map[string]any{
			"/api/users": map[string]any{
				"get": map[string]any{
					"responses": map[string]any{
						"200": map[string]any{"content": map[string]any{"application/json": map[string]any{"example": map[string]any{"v": 2}}}},
					},
				},
				"post": map[string]any{
					"responses": map[string]any{
						"201": map[string]any{"content": map[string]any{"application/json": map[string]any{"example": map[string]any{"id": 1}}}},
					},
				},
			},
			"bad-path": map[string]any{
				"get": map[string]any{"responses": map[string]any{"200": map[string]any{}}},
			},
		},
	}

	// 1) Preview classifies everything and persists nothing.
	status, env := doJSON(t, r, token, http.MethodPost,
		"/api/v1/projects/"+itoa(projectID)+"/swagger/preview", map[string]any{"document": doc})
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("preview status=%d env=%+v", status, env)
	}
	var preview struct {
		New       []map[string]any `json:"new"`
		Duplicate []struct {
			ExistingID string `json:"existingId"`
		} `json:"duplicate"`
		Invalid []map[string]any `json:"invalid"`
	}
	if err := json.Unmarshal(env.Data, &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if len(preview.New) != 1 || len(preview.Duplicate) != 1 || len(preview.Invalid) != 1 {
		t.Fatalf("preview classification = %+v", preview)
	}
	if preview.Duplicate[0].ExistingID != itoa(existingID) {
		t.Fatalf("duplicate existingId = %q, want %d", preview.Duplicate[0].ExistingID, existingID)
	}

	// Preview is side-effect free.
	if apis, _ := endpointRepo.ListByProject(projectID); len(apis) != 1 {
		t.Fatalf("preview persisted data: %d apis", len(apis))
	}

	// 2) Commit only the chosen entries: replace GET, create POST.
	commitBody := map[string]any{
		"document": doc,
		"selections": []map[string]string{
			{"method": "GET", "path": "/api/users", "action": "replace"},
			{"method": "POST", "path": "/api/users", "action": "create"},
		},
	}
	status, env = doJSON(t, r, token, http.MethodPost,
		"/api/v1/projects/"+itoa(projectID)+"/swagger/commit", commitBody)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("commit status=%d env=%+v", status, env)
	}
	var result struct {
		Created  int `json:"created"`
		Replaced int `json:"replaced"`
		Failed   []map[string]any
	}
	if err := json.Unmarshal(env.Data, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Created != 1 || result.Replaced != 1 || len(result.Failed) != 0 {
		t.Fatalf("commit result = %+v", result)
	}

	// Replacement kept the same row and thus the log association.
	replaced, err := endpointRepo.FindByID(existingID)
	if err != nil {
		t.Fatalf("replaced row missing: %v", err)
	}
	if replaced.ResponseBody != `{"v":2}` {
		t.Fatalf("replaced body = %q", replaced.ResponseBody)
	}
	logs, _, err := logRepo.ListByProject(projectID, 1, 10)
	if err != nil || len(logs) != 1 || logs[0].APIID != existingID {
		t.Fatalf("log not preserved after replace: %+v err=%v", logs, err)
	}
}

func TestSwaggerCommitRejectsEmptySelections(t *testing.T) {
	r, _, _, projectID, token := setupImportRouter(t)
	body := map[string]any{
		"document":   map[string]any{"paths": map[string]any{}},
		"selections": []any{},
	}
	status, env := doJSON(t, r, token, http.MethodPost,
		"/api/v1/projects/"+itoa(projectID)+"/swagger/commit", body)
	if status != http.StatusOK || env.Code == 0 {
		t.Fatalf("expected validation failure, status=%d env=%+v", status, env)
	}
}
