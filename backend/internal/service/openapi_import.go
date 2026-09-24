package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"github.com/mockhub/mockhub/internal/constants"
	"github.com/mockhub/mockhub/internal/dto"
	"github.com/mockhub/mockhub/internal/model"
	"github.com/mockhub/mockhub/internal/repository"
)

// openAPIEntry is a method + path pair successfully extracted from a document.
type openAPIEntry struct {
	Method       string
	Path         string
	StatusCode   int
	ResponseBody string
}

func openAPIKey(method, path string) string {
	return strings.ToUpper(method) + " " + path
}

// parseOpenAPI turns an OpenAPI 2.0/3.0 document into parseable entries and a
// list of fragments that could not be resolved. It performs no I/O and never
// writes anything, so it backs both the dry-run preview and the final commit.
func parseOpenAPI(doc any) ([]openAPIEntry, []dto.ImportInvalidItem, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, nil, constants.NewAppError(constants.CodeBadRequest, "无效的 OpenAPI 文档")
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, nil, constants.NewAppError(constants.CodeBadRequest, "无效的 OpenAPI JSON：文档不是合法的 JSON 对象")
	}
	pathsField, ok := parsed["paths"].(map[string]any)
	if !ok {
		return nil, nil, constants.NewAppError(constants.CodeBadRequest, "文档缺少 paths 字段，无法识别为 OpenAPI 文档")
	}

	entries := []openAPIEntry{}
	invalid := []dto.ImportInvalidItem{}
	seen := map[string]bool{}

	// Sort paths so preview ordering is deterministic.
	pathKeys := make([]string, 0, len(pathsField))
	for p := range pathsField {
		pathKeys = append(pathKeys, p)
	}
	sort.Strings(pathKeys)

	for _, path := range pathKeys {
		methodsAny, ok := pathsField[path].(map[string]any)
		if !ok {
			invalid = append(invalid, dto.ImportInvalidItem{
				Path: path, Reason: "路径定义不是对象，无法解析其操作",
			})
			continue
		}
		methodKeys := make([]string, 0, len(methodsAny))
		for m := range methodsAny {
			methodKeys = append(methodKeys, m)
		}
		sort.Strings(methodKeys)

		normalizedPath := normalizeOpenAPIPath(path)
		for _, methodKey := range methodKeys {
			if strings.HasPrefix(methodKey, "x-") {
				continue // OpenAPI extension, not an operation
			}
			method := strings.ToUpper(methodKey)
			if method == "PARAMETERS" {
				continue // path-level shared parameters
			}
			if !isHTTPMethod(method) {
				invalid = append(invalid, dto.ImportInvalidItem{
					Method: methodKey, Path: path,
					Reason: fmt.Sprintf("不支持的字段「%s」，仅支持 HTTP 方法（get/post/put/...）", methodKey),
				})
				continue
			}
			if !strings.HasPrefix(normalizedPath, "/") {
				invalid = append(invalid, dto.ImportInvalidItem{
					Method: method, Path: path, Reason: "路径必须以 / 开头",
				})
				continue
			}
			op, ok := methodsAny[methodKey].(map[string]any)
			if !ok {
				invalid = append(invalid, dto.ImportInvalidItem{
					Method: method, Path: path, Reason: "操作定义不是对象，无法解析",
				})
				continue
			}
			key := openAPIKey(method, normalizedPath)
			if seen[key] {
				invalid = append(invalid, dto.ImportInvalidItem{
					Method: method, Path: path, Reason: "同一方法和路径在文档中重复定义",
				})
				continue
			}
			seen[key] = true
			entries = append(entries, openAPIEntry{
				Method:       method,
				Path:         normalizedPath,
				StatusCode:   responseStatus(op),
				ResponseBody: responseBody(op),
			})
		}
	}
	return entries, invalid, nil
}

// normalizeOpenAPIPath converts OpenAPI {param} templates to the :param style
// used by the mock engine.
func normalizeOpenAPIPath(path string) string {
	parts := strings.Split(path, "/")
	for i, seg := range parts {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			parts[i] = ":" + seg[1:len(seg)-1]
		}
	}
	return strings.Join(parts, "/")
}

// responseStatus picks the status code of the first 2xx response (lowest code
// first), defaulting to 200.
func responseStatus(op map[string]any) int {
	responses, ok := op["responses"].(map[string]any)
	if !ok {
		return 200
	}
	codes := make([]int, 0, len(responses))
	for codeStr := range responses {
		if code := atoiSafe(codeStr); code >= 200 && code < 300 {
			codes = append(codes, code)
		}
	}
	if len(codes) == 0 {
		return 200
	}
	sort.Ints(codes)
	return codes[0]
}

// responseBody extracts a JSON example body from an operation, supporting both
// OpenAPI 3 (content/application/json) and Swagger 2.0 (produces/examples).
func responseBody(op map[string]any) string {
	if body := bodyFromResponsesV3(op); body != "" {
		return body
	}
	if body := bodyFromResponsesSwagger2(op); body != "" {
		return body
	}
	if body := bodyFromRequestBody(op); body != "" {
		return body
	}
	return `{"code":0,"message":"ok"}`
}

func bodyFromResponsesV3(op map[string]any) string {
	responses, ok := op["responses"].(map[string]any)
	if !ok {
		return ""
	}
	codeKeys := make([]string, 0, len(responses))
	for c := range responses {
		codeKeys = append(codeKeys, c)
	}
	sort.Strings(codeKeys)
	for _, codeStr := range codeKeys {
		if code := atoiSafe(codeStr); code < 200 || code >= 300 {
			continue
		}
		resp, ok := responses[codeStr].(map[string]any)
		if !ok {
			continue
		}
		if body := exampleFromContent(resp["content"]); body != "" {
			return body
		}
		if body := exampleFromSchema(resp["schema"]); body != "" {
			return body
		}
	}
	return ""
}

func bodyFromResponsesSwagger2(op map[string]any) string {
	responses, ok := op["responses"].(map[string]any)
	if !ok {
		return ""
	}
	codeKeys := make([]string, 0, len(responses))
	for c := range responses {
		codeKeys = append(codeKeys, c)
	}
	sort.Strings(codeKeys)
	for _, codeStr := range codeKeys {
		if code := atoiSafe(codeStr); code < 200 || code >= 300 {
			continue
		}
		resp, ok := responses[codeStr].(map[string]any)
		if !ok {
			continue
		}
		if examples, ok := resp["examples"].(map[string]any); ok {
			if ex, ok := examples["application/json"]; ok {
				return marshalExample(ex)
			}
			// Fall back to the first media type example.
			keys := make([]string, 0, len(examples))
			for k := range examples {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if b := marshalExample(examples[k]); b != "" {
					return b
				}
			}
		}
		if body := exampleFromSchema(resp["schema"]); body != "" {
			return body
		}
	}
	return ""
}

func bodyFromRequestBody(op map[string]any) string {
	reqBody, ok := op["requestBody"].(map[string]any)
	if !ok {
		return ""
	}
	return exampleFromContent(reqBody["content"])
}

// exampleFromContent resolves content["application/json"].example(s), falling
// back to the first media type that carries an example.
func exampleFromContent(contentAny any) string {
	content, ok := contentAny.(map[string]any)
	if !ok {
		return ""
	}
	if js, ok := content["application/json"].(map[string]any); ok {
		if body := exampleFromMedia(js); body != "" {
			return body
		}
	}
	mediaTypes := make([]string, 0, len(content))
	for mt := range content {
		mediaTypes = append(mediaTypes, mt)
	}
	sort.Strings(mediaTypes)
	for _, mt := range mediaTypes {
		if media, ok := content[mt].(map[string]any); ok {
			if body := exampleFromMedia(media); body != "" {
				return body
			}
		}
	}
	return ""
}

func exampleFromMedia(media map[string]any) string {
	if ex, ok := media["example"]; ok {
		return marshalExample(ex)
	}
	if examples, ok := media["examples"].(map[string]any); ok {
		names := make([]string, 0, len(examples))
		for n := range examples {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if holder, ok := examples[n].(map[string]any); ok {
				if ex, ok := holder["value"]; ok {
					return marshalExample(ex)
				}
			}
		}
	}
	if schema, ok := media["schema"]; ok {
		return exampleFromSchema(schema)
	}
	return ""
}

func exampleFromSchema(schemaAny any) string {
	schema, ok := schemaAny.(map[string]any)
	if !ok {
		return ""
	}
	if ex, ok := schema["example"]; ok {
		return marshalExample(ex)
	}
	return ""
}

func marshalExample(ex any) string {
	if ex == nil {
		return ""
	}
	if s, ok := ex.(string); ok {
		// A string example is usually already serialized JSON; keep it as-is
		// when it parses, otherwise marshal it as a JSON string.
		if json.Valid([]byte(s)) {
			return s
		}
	}
	b, err := json.Marshal(ex)
	if err != nil {
		return ""
	}
	return string(b)
}

// PreviewOpenAPI parses a document and classifies every entry against the
// endpoints already configured in the project. Nothing is persisted.
func (s *EndpointService) PreviewOpenAPI(projectID, userID uint, role string, doc any) (*dto.ImportPreview, error) {
	if _, err := s.checkAccess(projectID, userID, role); err != nil {
		return nil, err
	}
	entries, invalid, err := parseOpenAPI(doc)
	if err != nil {
		return nil, err
	}

	existing, err := s.endpoints.ListByProject(projectID)
	if err != nil {
		return nil, fmt.Errorf("load existing endpoints: %w", err)
	}
	existingByKey := make(map[string]*model.MockAPI, len(existing))
	for i := range existing {
		existingByKey[openAPIKey(existing[i].Method, existing[i].Path)] = &existing[i]
	}

	preview := &dto.ImportPreview{
		New:       []dto.ImportPreviewItem{},
		Duplicate: []dto.ImportPreviewItem{},
		Invalid:   invalid,
	}
	for _, e := range entries {
		item := dto.ImportPreviewItem{
			Method:       e.Method,
			Path:         e.Path,
			StatusCode:   e.StatusCode,
			ResponseBody: e.ResponseBody,
		}
		if current, ok := existingByKey[openAPIKey(e.Method, e.Path)]; ok {
			item.ExistingID = current.ID
			preview.Duplicate = append(preview.Duplicate, item)
		} else {
			preview.New = append(preview.New, item)
		}
	}
	s.logger.Info("openapi preview generated",
		"project_id", projectID,
		"new", len(preview.New), "duplicate", len(preview.Duplicate), "invalid", len(preview.Invalid))
	return preview, nil
}

// CommitOpenAPI writes only the entries selected in the preview. Each entry is
// validated independently and save failures are collected rather than aborting
// the whole import, so the caller can keep the preview and retry failed items.
func (s *EndpointService) CommitOpenAPI(projectID, userID uint, role string, doc any, selections []dto.SwaggerImportSelection) (*dto.ImportCommitResult, error) {
	if _, err := s.checkAccess(projectID, userID, role); err != nil {
		return nil, err
	}
	entries, invalid, err := parseOpenAPI(doc)
	if err != nil {
		return nil, err
	}

	parsedByKey := make(map[string]openAPIEntry, len(entries))
	for _, e := range entries {
		parsedByKey[openAPIKey(e.Method, e.Path)] = e
	}
	invalidKeys := make(map[string]bool, len(invalid))
	for _, f := range invalid {
		if f.Method != "" {
			invalidKeys[openAPIKey(f.Method, normalizeOpenAPIPath(f.Path))] = true
		}
	}

	// Re-check duplicates against live data in case configuration changed
	// between preview and commit.
	existing, err := s.endpoints.ListByProject(projectID)
	if err != nil {
		return nil, fmt.Errorf("load existing endpoints: %w", err)
	}
	existingByKey := make(map[string]*model.MockAPI, len(existing))
	for i := range existing {
		existingByKey[openAPIKey(existing[i].Method, existing[i].Path)] = &existing[i]
	}

	result := &dto.ImportCommitResult{Failed: []dto.ImportFailure{}}
	addFailure := func(sel dto.SwaggerImportSelection, reason string) {
		result.Failed = append(result.Failed, dto.ImportFailure{
			Method: sel.Method, Path: sel.Path, Action: sel.Action, Reason: reason,
		})
	}

	for _, sel := range selections {
		key := openAPIKey(sel.Method, sel.Path)
		current, exists := existingByKey[key]

		switch sel.Action {
		case dto.ImportActionSkip:
			result.Skipped++
			continue
		case dto.ImportActionCreate:
			if invalidKeys[key] {
				addFailure(sel, "该条目无法解析，不能导入")
				continue
			}
			entry, ok := parsedByKey[key]
			if !ok {
				addFailure(sel, "文档中不存在该条目，请重新生成预览")
				continue
			}
			if exists {
				addFailure(sel, "已存在相同方法和路径的接口，请选择替换或跳过")
				continue
			}
			api := &model.MockAPI{
				ProjectID:    projectID,
				Path:         entry.Path,
				Method:       entry.Method,
				StatusCode:   entry.StatusCode,
				ResponseBody: entry.ResponseBody,
			}
			if err := s.endpoints.Create(api); err != nil {
				s.logger.Warn("import create failed", "method", entry.Method, "path", entry.Path, "error", err)
				addFailure(sel, "保存失败："+humanizeSaveError(err))
				continue
			}
			existingByKey[key] = api
			result.Created++
		case dto.ImportActionReplace:
			if invalidKeys[key] {
				addFailure(sel, "该条目无法解析，不能导入")
				continue
			}
			entry, ok := parsedByKey[key]
			if !ok {
				addFailure(sel, "文档中不存在该条目，请重新生成预览")
				continue
			}
			if !exists {
				addFailure(sel, "未找到相同方法和路径的现有接口，无法替换")
				continue
			}
			// Update the existing row in place: its primary key (and therefore
			// the association of historical request logs) is preserved.
			current.StatusCode = entry.StatusCode
			current.ResponseBody = entry.ResponseBody
			if err := s.endpoints.Update(current); err != nil {
				s.logger.Warn("import replace failed", "method", entry.Method, "path", entry.Path, "error", err)
				addFailure(sel, "保存失败："+humanizeSaveError(err))
				continue
			}
			result.Replaced++
		default:
			addFailure(sel, "未知操作类型："+sel.Action)
		}
	}

	s.logger.Info("openapi import committed",
		"project_id", projectID,
		"created", result.Created, "replaced", result.Replaced, "skipped", result.Skipped, "failed", len(result.Failed))
	return result, nil
}

func humanizeSaveError(err error) string {
	if errors.Is(err, repository.ErrNotFound) {
		return "原接口已不存在"
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return "已存在相同方法和路径的接口，请选择替换或跳过"
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return "已存在相同方法和路径的接口，请选择替换或跳过"
	}
	return "数据库错误，请稍后重试"
}
