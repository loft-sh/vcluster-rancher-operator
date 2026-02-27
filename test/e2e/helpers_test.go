package e2e

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// createNamespace creates a namespace, retrying until the Rancher admission webhook is reachable.
func createNamespace(name string) {
	Eventually(func(g Gomega) {
		_, err := hostClient.CoreV1().Namespaces().Create(
			context.Background(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}},
			metav1.CreateOptions{},
		)
		if kerrors.IsAlreadyExists(err) {
			return
		}
		g.Expect(err).NotTo(HaveOccurred())
	}, 2*time.Minute, 5*time.Second).Should(Succeed())
}

// isReady returns true if the unstructured object has a condition type=Ready status=True.
func isReady(obj unstructured.Unstructured) bool {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == "Ready" && cond["status"] == "True" {
			return true
		}
	}
	return false
}

// conditionSummary returns a human-readable summary of status conditions for test output.
func conditionSummary(obj unstructured.Unstructured) string {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	out := ""
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		out += fmt.Sprintf("%s=%s ", cond["type"], cond["status"])
	}
	return out
}

// nestedString extracts a nested string field from an unstructured object.
func nestedString(obj unstructured.Unstructured, fields ...string) string {
	s, _, _ := unstructured.NestedString(obj.Object, fields...)
	return s
}
