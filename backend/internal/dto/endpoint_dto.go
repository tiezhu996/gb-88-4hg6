package dto

import "github.com/mockhub/mockhub/internal/model"

// EndpointRequest is the payload for creating/updating a mock endpoint.
type EndpointRequest struct {
	Path            string                `json:"path" validate:"required,startswith=/"`
	Method          string                `json:"method" validate:"required,oneof=GET POST PUT PATCH DELETE OPTIONS HEAD"`
	StatusCode      int                   `json:"statusCode" validate:"required,min=100,max=599"`
	ResponseBody    string                `json:"responseBody"`
	ResponseHeaders map[string]string     `json:"responseHeaders"`
	Delay           int                   `json:"delay" validate:"min=0,max=60000"`
	Conditions      []model.ConditionRule `json:"conditions"`
}

// Import action choices sent back by the preview page.
const (
	ImportActionCreate  = "create"
	ImportActionSkip    = "skip"
	ImportActionReplace = "replace"
)

// SwaggerImportRequest carries an OpenAPI 2.0/3.0 JSON document for preview.
type SwaggerImportRequest struct {
	Document any `json:"document" validate:"required"`
}

// SwaggerImportSelection is a single preview entry and the action chosen for it.
type SwaggerImportSelection struct {
	Method string `json:"method" validate:"required,oneof=GET POST PUT PATCH DELETE OPTIONS HEAD"`
	Path   string `json:"path" validate:"required,startswith=/"`
	Action string `json:"action" validate:"required,oneof=create skip replace"`
}

// SwaggerCommitRequest carries the document plus the user-confirmed selections.
// The server re-parses the document itself; selections only describe which
// parsed entries to write and how, so clients cannot forge endpoint content.
type SwaggerCommitRequest struct {
	Document   any                      `json:"document" validate:"required"`
	Selections []SwaggerImportSelection `json:"selections" validate:"required,min=1,dive"`
}

// ImportPreviewItem is a parsed entry that can be created or used as a
// replacement for an existing endpoint with the same method + path.
type ImportPreviewItem struct {
	Method       string `json:"method"`
	Path         string `json:"path"`
	StatusCode   int    `json:"statusCode"`
	ResponseBody string `json:"responseBody"`
	ExistingID   uint   `json:"existingId,string,omitempty"`
}

// ImportInvalidItem is a document fragment that could not be turned into an endpoint.
type ImportInvalidItem struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// ImportPreview groups parsed entries by how they relate to existing endpoints.
type ImportPreview struct {
	New       []ImportPreviewItem `json:"new"`
	Duplicate []ImportPreviewItem `json:"duplicate"`
	Invalid   []ImportInvalidItem `json:"invalid"`
}

// ImportFailure describes a single entry that could not be saved on commit.
type ImportFailure struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// ImportCommitResult summarizes a confirmed import, including per-entry
// failures so the caller can keep the preview and retry the failed ones.
type ImportCommitResult struct {
	Created  int             `json:"created"`
	Replaced int             `json:"replaced"`
	Skipped  int             `json:"skipped"`
	Failed   []ImportFailure `json:"failed"`
}
