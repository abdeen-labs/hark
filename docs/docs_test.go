package docs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var endpointHeading = regexp.MustCompile(`(?m)^#{3,4} \x60(GET|POST|PUT|PATCH|DELETE) (/[^\x60]+)\x60$`)

// TestOpenAPICoversTheCanonicalContract makes the two useful representations
// one contract rather than parallel documents that can drift. Every HTTP API
// endpoint named in the prose must have exactly one OpenAPI operation, and
// every OpenAPI operation must still be explained to a person.
func TestOpenAPICoversTheCanonicalContract(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal([]byte(OpenAPIContract), &spec); err != nil {
		t.Fatalf("openapi.json is not JSON: %v", err)
	}
	if got := fmt.Sprint(spec["openapi"]); !strings.HasPrefix(got, "3.1.") {
		t.Fatalf("openapi = %q, want 3.1.x", got)
	}

	want := make(map[string]bool)
	for _, match := range endpointHeading.FindAllStringSubmatch(APIContract, -1) {
		path := match[2]
		if path == "/healthz" || strings.HasPrefix(path, "/v1/") {
			want[strings.ToLower(match[1])+" "+path] = true
		}
	}

	paths := object(t, spec, "paths")
	got := make(map[string]bool)
	for path, raw := range paths {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Errorf("paths.%s is %T, want object", path, raw)
			continue
		}
		for _, method := range []string{"get", "post", "put", "patch", "delete"} {
			rawOperation, exists := item[method]
			if !exists {
				continue
			}
			key := method + " " + path
			got[key] = true
			op, ok := rawOperation.(map[string]any)
			if !ok {
				t.Errorf("%s is %T, want object", key, rawOperation)
				continue
			}
			for _, field := range []string{"summary", "operationId", "responses"} {
				if op[field] == nil {
					t.Errorf("%s has no %s", key, field)
				}
			}
		}
	}

	for operation := range want {
		if !got[operation] {
			t.Errorf("canonical Markdown documents %s but OpenAPI does not", operation)
		}
	}
	for operation := range got {
		if !want[operation] {
			t.Errorf("OpenAPI publishes %s but the canonical Markdown does not document it", operation)
		}
	}

	if len(got) < 40 {
		t.Fatalf("OpenAPI has only %d operations; route discovery is probably broken", len(got))
	}
	checkRefs(t, spec, spec, "$")
}

func object(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want object", key, parent[key])
	}
	return value
}

// checkRefs verifies local JSON pointers. A syntactically valid document with
// one misspelled schema name is still useless to a generator.
func checkRefs(t *testing.T, root map[string]any, value any, at string) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "$ref" {
				ref, ok := child.(string)
				if !ok || !strings.HasPrefix(ref, "#/") {
					t.Errorf("%s.$ref = %#v, want a local JSON pointer", at, child)
					continue
				}
				var current any = root
				for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
					obj, ok := current.(map[string]any)
					if !ok {
						current = nil
						break
					}
					current = obj[part]
				}
				if current == nil {
					t.Errorf("%s contains unresolved $ref %q", at, ref)
				}
			}
			checkRefs(t, root, child, at+"."+key)
		}
	case []any:
		for i, child := range value {
			checkRefs(t, root, child, fmt.Sprintf("%s[%d]", at, i))
		}
	}
}

func TestLLMsContractDiscoversEveryPublicFormat(t *testing.T) {
	for _, path := range []string{"/docs", "/docs.md", "/openapi.json", "/healthz"} {
		if !strings.Contains(LLMsContract, path) {
			t.Errorf("llms.txt does not point to %s", path)
		}
	}
}
