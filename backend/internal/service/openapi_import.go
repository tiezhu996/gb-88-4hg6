package service

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/mockhub/mockhub/internal/constants"
	"github.com/mockhub/mockhub/internal/dto"
	"github.com/mockhub/mockhub/internal/model"
)

// Preview entry statuses.
const (
	StatusNew       = "new"
	StatusDuplicate = "duplicate"
)

// Per-entry commit actions chosen by the user on the preview page.
const (
	ActionCreate  = "create"
	ActionReplace = "replace"
	ActionSkip    = "skip"
)

// pathItemKeywords are OpenAPI Path Item Object keys other than HTTP methods.
// They are part of the document structure and are ignored, not parse errors.
var pathItemKeywords = map[string]bool{
	"summary": true, "description": true, "servers": true,
	"parameters": true, "$ref": true,
}

// PreviewOpenAPI parses an OpenAPI 2.0/3.0 document without persisting
// anything and classifies every method + path pair as new or duplicate.
func (s *EndpointService) PreviewOpenAPI(projectID, userID uint, role string, doc any) (*dto.SwaggerPreviewResult, error) {
	if _, err := s.checkAccess(projectID, userID, role); err != nil {
		return nil, err
	}
	entries, invalid, err := parseOpenAPIDoc(doc)
	if err != nil {
		return nil, err
	}

	existing, err := s.endpoints.ListByProject(projectID)
	if err != nil {
		return nil, err
	}
	existingIndex := make(map[string]*model.MockAPI, len(existing))
	for i := range existing {
		existingIndex[endpointKey(existing[i].Method, existing[i].Path)] = &existing[i]
	}

	result := &dto.SwaggerPreviewResult{Invalid: invalid, InvalidCount: len(invalid)}
	for i := range entries {
		key := endpointKey(entries[i].Method, entries[i].Path)
		if dup, ok := existingIndex[key]; ok {
			entries[i].Status = StatusDuplicate
			entries[i].ExistingID = dup.ID
			result.DupCount++
		} else {
			result.NewCount++
		}
		result.Entries = append(result.Entries, entries[i])
	}
	return result, nil
}

// CommitOpenAPI re-parses the document and applies only the entries the user
// selected on the preview page. Replacements update the existing row in place
// so its ID — and therefore its historical request logs — stay linked. Each
// entry is saved independently: failures are returned per entry and
// successfully saved entries are kept (partial success).
func (s *EndpointService) CommitOpenAPI(projectID, userID uint, role string, req dto.SwaggerCommitRequest) (*dto.SwaggerCommitResult, error) {
	if _, err := s.checkAccess(projectID, userID, role); err != nil {
		return nil, err
	}
	entries, _, err := parseOpenAPIDoc(req.Document)
	if err != nil {
		return nil, err
	}
	parsedIndex := make(map[string]dto.SwaggerPreviewEntry, len(entries))
	for _, e := range entries {
		parsedIndex[endpointKey(e.Method, e.Path)] = e
	}

	existing, err := s.endpoints.ListByProject(projectID)
	if err != nil {
		return nil, err
	}
	existingIndex := make(map[string]*model.MockAPI, len(existing))
	for i := range existing {
		existingIndex[endpointKey(existing[i].Method, existing[i].Path)] = &existing[i]
	}

	result := &dto.SwaggerCommitResult{}
	addFailure := func(sel dto.SwaggerImportSelection, reason string) {
		result.Failed++
		result.Failures = append(result.Failures, dto.SwaggerImportFailure{
			Method: sel.Method,
			Path:   sel.Path,
			Action: sel.Action,
			Reason: reason,
		})
	}

	for _, sel := range req.Selections {
		if sel.Action == ActionSkip {
			result.Skipped++
			continue
		}
		key := endpointKey(sel.Method, sel.Path)
		entry, ok := parsedIndex[key]
		if !ok {
			addFailure(sel, "文档中不存在该条目或该条目无法解析")
			continue
		}
		current := existingIndex[key]

		switch sel.Action {
		case ActionCreate:
			if current != nil {
				// Idempotent retry: the endpoint now exists with the exact
				// content parsed from the document (e.g. a previous attempt
				// created it before reporting a sibling failure). Treat it as
				// applied; content drift is still surfaced as a failure so a
				// duplicate is never silently overwritten.
				if sameEndpointContent(current, entry) {
					result.Created++
					continue
				}
				addFailure(sel, "同路径同方法的接口已存在，请选择「替换」或「跳过」")
				continue
			}
			e := &model.MockAPI{
				ProjectID:       projectID,
				Path:            entry.Path,
				Method:          entry.Method,
				StatusCode:      entry.StatusCode,
				ResponseBody:    entry.ResponseBody,
				ResponseHeaders: map[string]string{},
				Conditions:      []model.ConditionRule{},
			}
			if err := s.endpoints.Create(e); err != nil {
				s.logger.Warn("import create endpoint failed", "project_id", projectID, "method", e.Method, "path", e.Path, "error", err)
				addFailure(sel, "保存失败，请稍后重试")
				continue
			}
			existingIndex[key] = e
			result.Created++
		case ActionReplace:
			if current == nil {
				addFailure(sel, "同路径同方法的接口不存在，无法替换，请选择「新增」")
				continue
			}
			// Idempotent retry: a previous attempt already replaced this row
			// with exactly the parsed content — report it as applied instead
			// of rewriting it again.
			if sameEndpointContent(current, entry) {
				result.Replaced++
				continue
			}
			// Update in place: keep the primary key (and CreatedAt) so that
			// request logs recorded against this endpoint remain associated.
			current.Path = entry.Path
			current.Method = entry.Method
			current.StatusCode = entry.StatusCode
			current.ResponseBody = entry.ResponseBody
			current.ResponseHeaders = map[string]string{}
			current.Conditions = []model.ConditionRule{}
			current.Delay = 0
			if err := s.endpoints.Update(current); err != nil {
				s.logger.Warn("import replace endpoint failed", "project_id", projectID, "endpoint_id", current.ID, "method", current.Method, "path", current.Path, "error", err)
				addFailure(sel, "保存失败，请稍后重试")
				continue
			}
			result.Replaced++
		default:
			addFailure(sel, "不支持的操作类型: "+sel.Action)
		}
	}

	s.logger.Info("openapi import committed",
		"project_id", projectID,
		"created", result.Created,
		"replaced", result.Replaced,
		"skipped", result.Skipped,
		"failed", result.Failed,
	)
	return result, nil
}

// parseOpenAPIDoc extracts every method + path pair from an OpenAPI 2.0/3.0
// document. Valid pairs are returned as preview entries (sorted for a stable
// preview); malformed pairs are returned as invalid entries with a
// human-readable reason.
func parseOpenAPIDoc(doc any) ([]dto.SwaggerPreviewEntry, []dto.SwaggerInvalidEntry, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, nil, constants.NewAppError(constants.CodeBadRequest, "无效的 OpenAPI 文档")
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, nil, constants.NewAppError(constants.CodeBadRequest, "无效的 OpenAPI JSON")
	}
	pathsMap, ok := parsed["paths"].(map[string]any)
	if !ok {
		return nil, nil, constants.NewAppError(constants.CodeBadRequest, "文档缺少 paths 字段")
	}

	entries := []dto.SwaggerPreviewEntry{}
	invalid := []dto.SwaggerInvalidEntry{}

	for _, path := range sortedMapKeys(pathsMap) {
		if path == "" || !strings.HasPrefix(path, "/") {
			invalid = append(invalid, dto.SwaggerInvalidEntry{Path: displayPath(path), Reason: "路径必须以 / 开头"})
			continue
		}
		methods, ok := pathsMap[path].(map[string]any)
		if !ok {
			invalid = append(invalid, dto.SwaggerInvalidEntry{Path: path, Reason: "路径定义格式不正确，应为对象"})
			continue
		}
		for _, methodKey := range sortedMapKeys(methods) {
			if pathItemKeywords[methodKey] {
				continue
			}
			method := strings.ToUpper(methodKey)
			if !isHTTPMethod(method) {
				invalid = append(invalid, dto.SwaggerInvalidEntry{Path: path, Method: methodKey, Reason: "不支持的请求方法: " + methodKey})
				continue
			}
			op, ok := methods[methodKey].(map[string]any)
			if !ok {
				invalid = append(invalid, dto.SwaggerInvalidEntry{Path: path, Method: method, Reason: "操作定义格式不正确，应为对象"})
				continue
			}
			entries = append(entries, buildImportEntry(method, path, op))
		}
	}
	return entries, invalid, nil
}

// buildImportEntry resolves status code and JSON example body for one operation.
func buildImportEntry(method, path string, op map[string]any) dto.SwaggerPreviewEntry {
	body := requestBodyExample(op)
	status := 200
	if responses, ok := op["responses"].(map[string]any); ok {
		// Deterministic status code: the first declared 2xx code wins.
		for _, codeStr := range sortedMapKeys(responses) {
			code := atoiSafe(codeStr)
			if code >= 200 && code < 300 {
				status = code
				if resp, ok := responses[codeStr].(map[string]any); ok {
					if ex := responseExample(resp); ex != "" {
						body = ex
					}
				}
				break
			}
		}
	}
	return dto.SwaggerPreviewEntry{
		Method:       method,
		Path:         path,
		StatusCode:   status,
		ResponseBody: body,
		Status:       StatusNew,
	}
}

// requestBodyExample extracts the OpenAPI 3 request body JSON example.
func requestBodyExample(op map[string]any) string {
	if reqBody, ok := op["requestBody"].(map[string]any); ok {
		if content, ok := reqBody["content"].(map[string]any); ok {
			if js, ok := content["application/json"].(map[string]any); ok {
				if ex, ok := js["example"]; ok {
					b, _ := json.Marshal(ex)
					return string(b)
				}
			}
		}
	}
	return `{"code":0,"message":"ok"}`
}

// responseExample extracts the JSON example of one response, supporting both
// OpenAPI 3 (content.application/json.example) and Swagger 2.0
// (examples.application/json).
func responseExample(resp map[string]any) string {
	if content, ok := resp["content"].(map[string]any); ok {
		if js, ok := content["application/json"].(map[string]any); ok {
			if ex, ok := js["example"]; ok {
				b, _ := json.Marshal(ex)
				return string(b)
			}
		}
	}
	if examples, ok := resp["examples"].(map[string]any); ok {
		if ex, ok := examples["application/json"]; ok {
			b, _ := json.Marshal(ex)
			return string(b)
		}
	}
	return ""
}

func endpointKey(method, path string) string {
	return strings.ToUpper(method) + " " + path
}

// sameEndpointContent reports whether an existing endpoint already carries the
// content the import would write, used for idempotent commit retries.
func sameEndpointContent(e *model.MockAPI, entry dto.SwaggerPreviewEntry) bool {
	return strings.EqualFold(e.Method, entry.Method) &&
		e.Path == entry.Path &&
		e.StatusCode == entry.StatusCode &&
		e.ResponseBody == entry.ResponseBody
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func displayPath(path string) string {
	if path == "" {
		return "(空路径)"
	}
	return path
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
