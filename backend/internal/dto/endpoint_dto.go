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

// SwaggerPreviewRequest carries an OpenAPI 2.0/3.0 JSON document that the
// backend parses for a non-persistent import preview.
type SwaggerPreviewRequest struct {
	Document any `json:"document" validate:"required"`
}

// SwaggerPreviewEntry is one method + path pair parsed from the document.
type SwaggerPreviewEntry struct {
	Method       string `json:"method"`
	Path         string `json:"path"`
	StatusCode   int    `json:"statusCode"`
	ResponseBody string `json:"responseBody"`
	Status       string `json:"status"`
	ExistingID   uint   `json:"existingId,omitempty"`
}

// SwaggerInvalidEntry describes a path item that could not be parsed into an
// endpoint, with the reason shown verbatim to the user.
type SwaggerInvalidEntry struct {
	Path   string `json:"path"`
	Method string `json:"method,omitempty"`
	Reason string `json:"reason"`
}

// SwaggerPreviewResult lists every parsed entry classified as new or
// duplicate, plus entries that could not be parsed. Nothing is persisted.
type SwaggerPreviewResult struct {
	Entries      []SwaggerPreviewEntry `json:"entries"`
	Invalid      []SwaggerInvalidEntry `json:"invalid"`
	NewCount     int                   `json:"newCount"`
	DupCount     int                   `json:"dupCount"`
	InvalidCount int                   `json:"invalidCount"`
}

// SwaggerImportSelection is one entry the user chose to apply on confirm.
type SwaggerImportSelection struct {
	Method string `json:"method" validate:"required"`
	Path   string `json:"path" validate:"required"`
	Action string `json:"action" validate:"required,oneof=create replace skip"`
}

// SwaggerCommitRequest carries the original document again together with the
// per-entry selections made on the preview page. The document is re-parsed
// server-side so client-supplied endpoint content is never trusted.
type SwaggerCommitRequest struct {
	Document   any                      `json:"document" validate:"required"`
	Selections []SwaggerImportSelection `json:"selections" validate:"required,min=1,dive"`
}

// SwaggerImportFailure identifies an entry that could not be saved and why.
type SwaggerImportFailure struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// SwaggerCommitResult summarizes a partial-success import. Successful entries
// are already persisted; the caller keeps the preview and retries failures.
type SwaggerCommitResult struct {
	Created  int                    `json:"created"`
	Replaced int                    `json:"replaced"`
	Skipped  int                    `json:"skipped"`
	Failed   int                    `json:"failed"`
	Failures []SwaggerImportFailure `json:"failures"`
}
