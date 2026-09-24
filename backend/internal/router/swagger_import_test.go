package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/mockhub/mockhub/internal/config"
	"github.com/mockhub/mockhub/internal/handler"
	"github.com/mockhub/mockhub/internal/model"
	"github.com/mockhub/mockhub/internal/repository"
	"github.com/mockhub/mockhub/internal/service"
	"github.com/mockhub/mockhub/internal/util"
)

func init() {
	gin.SetMode(gin.TestMode)
}

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// setupImportRouter wires the real router against an in-memory SQLite database
// and returns the engine, the owning user's JWT and the seeded project id.
func setupImportRouter(t *testing.T) (*gin.Engine, *gorm.DB, string, uint) {
	t.Helper()
	db := newIntegrationDB(t)

	projectRepo := repository.NewProjectRepository(db)
	endpointRepo := repository.NewEndpointRepository(db)
	logRepo := repository.NewRequestLogRepository(db)

	cfg := &config.Config{
		Env:                "test",
		JWTSecret:          "integration-secret-that-is-long-enough",
		JWTExpire:          1,
		CORSAllowedOrigins: "http://localhost:8119",
		AuthRateLimit:      1000,
		MockRateLimit:      1000,
	}
	logger := discardSlog(t)

	owner := &model.User{Username: "owner_router", PasswordHash: "x", Role: "dev"}
	if err := db.Create(owner).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	project, err := service.NewProjectService(projectRepo, logger).
		Create("p", "d", owner.ID, "http://localhost:3119")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	// Pre-existing duplicate endpoint.
	if err := endpointRepo.Create(&model.MockAPI{
		ProjectID: project.ID, Path: "/api/orders", Method: "GET", StatusCode: 200,
	}); err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}

	token, err := util.GenerateToken(cfg.JWTSecret, owner.ID, "dev", 1e9)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	h := &Handlers{
		Health:     handler.NewHealthHandler(db, logger),
		Endpoint:   handler.NewEndpointHandler(service.NewEndpointService(projectRepo, endpointRepo, logger), logger),
		RequestLog: handler.NewRequestLogHandler(service.NewRequestLogService(projectRepo, logRepo, logger), logger),
	}
	return New(cfg, logger, h), db, token, project.ID
}

func doJSON(t *testing.T, engine *gin.Engine, method, path, token string, body any) envelope {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s status = %d, body = %s", method, path, rec.Code, rec.Body.String())
	}
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v; body=%s", err, rec.Body.String())
	}
	return env
}

func TestSwaggerPreviewCommitEndToEnd(t *testing.T) {
	engine, db, token, projectID := setupImportRouter(t)

	doc := map[string]any{
		"openapi": "3.0.0",
		"paths": map[string]any{
			"/api/orders": map[string]any{
				"get":  map[string]any{"responses": map[string]any{"200": map[string]any{"content": map[string]any{"application/json": map[string]any{"example": map[string]any{"v": "new"}}}}}},
				"post": map[string]any{"responses": map[string]any{"201": map[string]any{"content": map[string]any{"application/json": map[string]any{"example": map[string]any{"id": 7}}}}}},
			},
			"/bad": map[string]any{"trace": map[string]any{}},
		},
	}

	// 1. Preview is non-persistent: counts are new=1 duplicate=1 invalid=1.
	previewEnv := doJSON(t, engine, http.MethodPost,
		"/api/v1/projects/"+itoa(projectID)+"/swagger/preview", token, map[string]any{"document": doc})
	if previewEnv.Code != 0 {
		t.Fatalf("preview code = %d, message = %s", previewEnv.Code, previewEnv.Message)
	}
	var preview struct {
		NewCount     int `json:"newCount"`
		DupCount     int `json:"dupCount"`
		InvalidCount int `json:"invalidCount"`
		Entries      []struct {
			Method     string `json:"method"`
			Path       string `json:"path"`
			Status     string `json:"status"`
			ExistingID uint   `json:"existingId"`
		} `json:"entries"`
		Invalid []struct {
			Path   string `json:"path"`
			Method string `json:"method"`
			Reason string `json:"reason"`
		} `json:"invalid"`
	}
	if err := json.Unmarshal(previewEnv.Data, &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if preview.NewCount != 1 || preview.DupCount != 1 || preview.InvalidCount != 1 {
		t.Fatalf("preview counts = %+v", preview)
	}
	if len(preview.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(preview.Entries))
	}
	var dupExistingID uint
	for _, e := range preview.Entries {
		if e.Method == "GET" && e.Path == "/api/orders" {
			if e.Status != "duplicate" || e.ExistingID == 0 {
				t.Fatalf("GET /api/orders preview = %+v, want duplicate", e)
			}
			dupExistingID = e.ExistingID
		}
	}
	if len(preview.Invalid) != 1 || preview.Invalid[0].Reason == "" {
		t.Fatalf("invalid = %+v", preview.Invalid)
	}

	// Nothing was persisted by preview itself.
	var count int64
	db.Model(&model.MockAPI{}).Where("project_id = ?", projectID).Count(&count)
	if count != 1 {
		t.Fatalf("endpoints after preview = %d, want 1 (preview must not persist)", count)
	}

	// A historical request log references the endpoint that will be replaced.
	if err := db.Create(&model.RequestLog{
		ProjectID: projectID, APIID: dupExistingID, Method: "GET", Path: "/api/orders", ResponseStatus: 200,
	}).Error; err != nil {
		t.Fatalf("create log: %v", err)
	}

	// 2. Commit: replace duplicate + create new; invalid entry is not sent.
	commitEnv := doJSON(t, engine, http.MethodPost,
		"/api/v1/projects/"+itoa(projectID)+"/swagger/commit", token,
		map[string]any{
			"document": doc,
			"selections": []map[string]string{
				{"method": "GET", "path": "/api/orders", "action": "replace"},
				{"method": "POST", "path": "/api/orders", "action": "create"},
			},
		})
	if commitEnv.Code != 0 {
		t.Fatalf("commit code = %d, message = %s", commitEnv.Code, commitEnv.Message)
	}
	var result struct {
		Created  int `json:"created"`
		Replaced int `json:"replaced"`
		Failed   int `json:"failed"`
	}
	if err := json.Unmarshal(commitEnv.Data, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Created != 1 || result.Replaced != 1 || result.Failed != 0 {
		t.Fatalf("commit result = %+v", result)
	}

	// 3. Replacement kept the same row id; the historical log still links to it.
	var replaced model.MockAPI
	if err := db.First(&replaced, dupExistingID).Error; err != nil {
		t.Fatalf("reload replaced endpoint: %v", err)
	}
	if replaced.ResponseBody != `{"v":"new"}` {
		t.Fatalf("replaced body = %q, want new example", replaced.ResponseBody)
	}
	var log model.RequestLog
	if err := db.First(&log).Error; err != nil {
		t.Fatalf("reload log: %v", err)
	}
	if log.APIID != dupExistingID {
		t.Fatalf("log apiId = %d, want %d after replace", log.APIID, dupExistingID)
	}
}

func TestSwaggerPreviewRequiresAuth(t *testing.T) {
	engine, _, _, projectID := setupImportRouter(t)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/projects/"+itoa(projectID)+"/swagger/preview",
		bytes.NewReader([]byte(`{"document":{"paths":{}}}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
