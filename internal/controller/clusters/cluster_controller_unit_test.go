package clusters

import (
	"testing"

	v1unstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func conditions(kvs ...string) map[string]any {
	cond := map[string]any{}
	for i := 0; i+1 < len(kvs); i += 2 {
		cond[kvs[i]] = kvs[i+1]
	}
	return map[string]any{
		"status": map[string]any{
			"conditions": []any{cond},
		},
	}
}

func TestIsClusterReady(t *testing.T) {
	tests := []struct {
		name     string
		object   map[string]any
		expected bool
	}{
		{"no status", nil, false},
		{"empty conditions", map[string]any{"status": map[string]any{"conditions": []any{}}}, false},
		{"Ready True", conditions("type", "Ready", "status", "True"), true},
		{"Ready False", conditions("type", "Ready", "status", "False"), false},
		{"other condition only", conditions("type", "Initialized", "status", "True"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := v1unstructured.Unstructured{}
			if tt.object != nil {
				cluster.Object = tt.object
			}

			if got := isClusterReady(cluster); got != tt.expected {
				t.Errorf("isClusterReady() = %v, want %v", got, tt.expected)
			}
		})
	}
}
