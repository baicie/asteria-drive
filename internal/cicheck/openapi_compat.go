package cicheck

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var openAPIMethods = map[string]struct{}{
	"delete": {}, "get": {}, "head": {}, "options": {}, "patch": {}, "post": {}, "put": {}, "trace": {},
}

func VerifyOpenAPICompatibility(basePath, currentPath string) error {
	return compareOpenAPICompatibility(basePath, currentPath)
}

func compareOpenAPICompatibility(basePath, currentPath string) error {
	base, err := loadOpenAPIDocument(basePath)
	if err != nil {
		return fmt.Errorf("load base OpenAPI document: %w", err)
	}
	current, err := loadOpenAPIDocument(currentPath)
	if err != nil {
		return fmt.Errorf("load current OpenAPI document: %w", err)
	}
	basePaths := mapValue(base["paths"])
	currentPaths := mapValue(current["paths"])
	if basePaths == nil || currentPaths == nil {
		return fmt.Errorf("both OpenAPI documents must define paths")
	}
	var problems []string
	for _, path := range sortedKeys(basePaths) {
		baseItem := mapValue(basePaths[path])
		currentItem, ok := currentPaths[path]
		if !ok {
			problems = append(problems, fmt.Sprintf("path %s was removed", path))
			continue
		}
		currentItemMap := mapValue(currentItem)
		for _, method := range sortedKeys(baseItem) {
			if _, isOperation := openAPIMethods[method]; !isOperation {
				continue
			}
			baseOperation := mapValue(baseItem[method])
			currentOperation, ok := currentItemMap[method]
			if !ok {
				problems = append(problems, fmt.Sprintf("operation %s %s was removed", strings.ToUpper(method), path))
				continue
			}
			problems = append(problems, compareOperation(path, method, baseOperation, mapValue(currentOperation))...)
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("breaking OpenAPI changes:\n- %s", strings.Join(problems, "\n- "))
}

func loadOpenAPIDocument(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	return document, nil
}

func compareOperation(path, method string, base, current map[string]any) []string {
	var problems []string
	baseParameters := parameterMap(base["parameters"])
	currentParameters := parameterMap(current["parameters"])
	for identity, parameter := range baseParameters {
		currentParameter, ok := currentParameters[identity]
		if !ok {
			problems = append(problems, fmt.Sprintf("parameter %s was removed from %s %s", identity, strings.ToUpper(method), path))
			continue
		}
		if !boolValue(parameter["required"]) && boolValue(currentParameter["required"]) {
			problems = append(problems, fmt.Sprintf("parameter %s became required on %s %s", identity, strings.ToUpper(method), path))
		}
	}
	baseRequest := mapValue(base["requestBody"])
	currentRequest := mapValue(current["requestBody"])
	if baseRequest != nil && currentRequest == nil {
		problems = append(problems, fmt.Sprintf("request body was removed from %s %s", strings.ToUpper(method), path))
	} else if baseRequest != nil && !boolValue(baseRequest["required"]) && boolValue(currentRequest["required"]) {
		problems = append(problems, fmt.Sprintf("request body became required on %s %s", strings.ToUpper(method), path))
	}
	baseResponses := mapValue(base["responses"])
	currentResponses := mapValue(current["responses"])
	for status, response := range baseResponses {
		currentResponse, ok := currentResponses[status]
		if !ok {
			problems = append(problems, fmt.Sprintf("response %s was removed from %s %s", status, strings.ToUpper(method), path))
			continue
		}
		problems = append(problems, compareResponseSchema(path, method, status, mapValue(response), mapValue(currentResponse))...)
	}
	return problems
}

func compareResponseSchema(path, method, status string, base, current map[string]any) []string {
	baseContent := mapValue(base["content"])
	currentContent := mapValue(current["content"])
	var problems []string
	for mediaType, baseMedia := range baseContent {
		currentMedia, ok := currentContent[mediaType]
		if !ok {
			problems = append(problems, fmt.Sprintf("response %s media type %s was removed from %s %s", status, mediaType, strings.ToUpper(method), path))
			continue
		}
		baseSchema := mapValue(mapValue(baseMedia)["schema"])
		currentSchema := mapValue(mapValue(currentMedia)["schema"])
		problems = append(problems, compareSchema(fmt.Sprintf("response %s of %s %s", status, strings.ToUpper(method), path), baseSchema, currentSchema)...)
	}
	return problems
}

func compareSchema(location string, base, current map[string]any) []string {
	if base == nil {
		return nil
	}
	if current == nil {
		return []string{fmt.Sprintf("schema was removed from %s", location)}
	}
	var problems []string
	if baseRef, ok := base["$ref"].(string); ok {
		if currentRef, ok := current["$ref"].(string); !ok || currentRef != baseRef {
			problems = append(problems, fmt.Sprintf("schema reference changed at %s", location))
		}
	}
	if baseType, ok := base["type"].(string); ok {
		if currentType, ok := current["type"].(string); !ok || currentType != baseType {
			problems = append(problems, fmt.Sprintf("schema type changed at %s", location))
		}
	}
	baseRequired := stringSet(base["required"])
	currentRequired := stringSet(current["required"])
	for name := range baseRequired {
		if !currentRequired[name] {
			problems = append(problems, fmt.Sprintf("required property %s was removed from %s", name, location))
		}
	}
	baseProperties := mapValue(base["properties"])
	currentProperties := mapValue(current["properties"])
	for name, baseProperty := range baseProperties {
		currentProperty, ok := currentProperties[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("property %s was removed from %s", name, location))
			continue
		}
		problems = append(problems, compareSchema(location+"."+name, mapValue(baseProperty), mapValue(currentProperty))...)
	}
	return problems
}

func parameterMap(value any) map[string]map[string]any {
	result := make(map[string]map[string]any)
	items, ok := value.([]any)
	if !ok {
		return result
	}
	for _, item := range items {
		parameter := mapValue(item)
		name, nameOK := parameter["name"].(string)
		location, locationOK := parameter["in"].(string)
		if nameOK && locationOK {
			result[name+" in "+location] = parameter
		}
	}
	return result
}

func mapValue(value any) map[string]any {
	if value == nil {
		return nil
	}
	result, _ := value.(map[string]any)
	return result
}

func stringSet(value any) map[string]bool {
	result := make(map[string]bool)
	items, ok := value.([]any)
	if !ok {
		return result
	}
	for _, item := range items {
		if name, ok := item.(string); ok {
			result[name] = true
		}
	}
	return result
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
