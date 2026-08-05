package server

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPIOperationsMatchRegisteredRoutes(t *testing.T) {
	t.Parallel()

	documentBytes, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatalf("read OpenAPI document: %v", err)
	}
	var document struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(documentBytes, &document); err != nil {
		t.Fatalf("parse OpenAPI document: %v", err)
	}

	documented := make(map[string]struct{})
	for path, pathItem := range document.Paths {
		for method := range pathItem {
			method = strings.ToUpper(method)
			if !isHTTPMethod(method) {
				continue
			}
			documented[method+" "+path] = struct{}{}
		}
	}

	registered := make(map[string]struct{}, len(publicAPIRoutes)+len(externalAPIRoutes)+len(protectedAPIRoutes))
	for _, route := range appendRoutes(publicAPIRoutes, externalAPIRoutes, protectedAPIRoutes) {
		if _, exists := registered[route.pattern]; exists {
			t.Fatalf("route %q is registered more than once", route.pattern)
		}
		registered[route.pattern] = struct{}{}
	}

	if missing := setDifference(registered, documented); len(missing) != 0 {
		t.Errorf("registered routes missing from OpenAPI: %s", strings.Join(missing, ", "))
	}
	if extra := setDifference(documented, registered); len(extra) != 0 {
		t.Errorf("OpenAPI operations missing from server routes: %s", strings.Join(extra, ", "))
	}
}

func appendRoutes(groups ...[]apiRoute) []apiRoute {
	var routes []apiRoute
	for _, group := range groups {
		routes = append(routes, group...)
	}
	return routes
}

func isHTTPMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

func setDifference(left, right map[string]struct{}) []string {
	difference := make([]string, 0)
	for value := range left {
		if _, ok := right[value]; !ok {
			difference = append(difference, fmt.Sprintf("%q", value))
		}
	}
	sort.Strings(difference)
	return difference
}
