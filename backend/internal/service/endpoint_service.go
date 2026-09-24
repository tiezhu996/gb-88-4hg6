package service

import (
	"log/slog"

	"github.com/mockhub/mockhub/internal/dto"
	"github.com/mockhub/mockhub/internal/model"
	"github.com/mockhub/mockhub/internal/repository"
)

// EndpointService manages mock endpoints and OpenAPI imports.
type EndpointService struct {
	projects  *repository.ProjectRepository
	endpoints *repository.EndpointRepository
	logger    *slog.Logger
}

// NewEndpointService builds an EndpointService.
func NewEndpointService(projects *repository.ProjectRepository, endpoints *repository.EndpointRepository, logger *slog.Logger) *EndpointService {
	return &EndpointService{projects: projects, endpoints: endpoints, logger: logger}
}

// List returns endpoints of a project after checking access.
func (s *EndpointService) List(projectID, userID uint, role string) ([]model.MockAPI, error) {
	if _, err := s.checkAccess(projectID, userID, role); err != nil {
		return nil, err
	}
	return s.endpoints.ListByProject(projectID)
}

// Get loads a single endpoint after checking access.
func (s *EndpointService) Get(projectID, id, userID uint, role string) (*model.MockAPI, error) {
	if _, err := s.checkAccess(projectID, userID, role); err != nil {
		return nil, err
	}
	e, err := s.endpoints.FindByID(id)
	if err != nil {
		return nil, err
	}
	if e.ProjectID != projectID {
		return nil, repository.ErrNotFound
	}
	return e, nil
}

// Create adds an endpoint to a project.
func (s *EndpointService) Create(projectID, userID uint, role string, req dto.EndpointRequest) (*model.MockAPI, error) {
	if _, err := s.checkAccess(projectID, userID, role); err != nil {
		return nil, err
	}
	e := &model.MockAPI{
		ProjectID:       projectID,
		Path:            req.Path,
		Method:          req.Method,
		StatusCode:      req.StatusCode,
		ResponseBody:    req.ResponseBody,
		ResponseHeaders: req.ResponseHeaders,
		Delay:           req.Delay,
		Conditions:      req.Conditions,
	}
	if err := s.endpoints.Create(e); err != nil {
		return nil, err
	}
	s.logger.Info("endpoint created", "project_id", projectID, "endpoint_id", e.ID, "method", e.Method, "path", e.Path)
	return e, nil
}

// Update edits an endpoint after checking access.
func (s *EndpointService) Update(projectID, id, userID uint, role string, req dto.EndpointRequest) (*model.MockAPI, error) {
	e, err := s.Get(projectID, id, userID, role)
	if err != nil {
		return nil, err
	}
	e.Path = req.Path
	e.Method = req.Method
	e.StatusCode = req.StatusCode
	e.ResponseBody = req.ResponseBody
	e.ResponseHeaders = req.ResponseHeaders
	e.Delay = req.Delay
	e.Conditions = req.Conditions
	if err := s.endpoints.Update(e); err != nil {
		return nil, err
	}
	return e, nil
}

// Delete removes an endpoint after checking access.
func (s *EndpointService) Delete(projectID, id, userID uint, role string) error {
	if _, err := s.Get(projectID, id, userID, role); err != nil {
		return err
	}
	if err := s.endpoints.Delete(id); err != nil {
		return err
	}
	s.logger.Info("endpoint deleted", "project_id", projectID, "endpoint_id", id)
	return nil
}

// ImportOpenAPI is kept for compatibility with callers that expect the old
// one-shot behavior. New clients should use PreviewOpenAPI + CommitOpenAPI so
// users can inspect new/duplicate/invalid entries before anything is written.
func (s *EndpointService) ImportOpenAPI(projectID, userID uint, role string, doc any) (int, error) {
	preview, err := s.PreviewOpenAPI(projectID, userID, role, doc)
	if err != nil {
		return 0, err
	}
	selections := make([]dto.SwaggerImportSelection, 0, len(preview.New)+len(preview.Duplicate))
	for _, item := range preview.New {
		selections = append(selections, dto.SwaggerImportSelection{
			Method: item.Method, Path: item.Path, Action: dto.ImportActionCreate,
		})
	}
	// The legacy endpoint skipped duplicates silently.
	for _, item := range preview.Duplicate {
		selections = append(selections, dto.SwaggerImportSelection{
			Method: item.Method, Path: item.Path, Action: dto.ImportActionSkip,
		})
	}
	if len(selections) == 0 {
		return 0, nil
	}
	result, err := s.CommitOpenAPI(projectID, userID, role, doc, selections)
	if err != nil {
		return 0, err
	}
	return result.Created, nil
}

func (s *EndpointService) checkAccess(projectID, userID uint, role string) (*model.Project, error) {
	return checkProjectAccess(s.projects, projectID, userID, role)
}

func isHTTPMethod(s string) bool {
	switch s {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD":
		return true
	}
	return false
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
