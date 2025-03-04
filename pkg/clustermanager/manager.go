package clustermanager

import (
	"context"
	"crypto/sha256"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Manager struct {
	LocalClient   client.Client
	ClusterClient client.Client
}

func (m *Manager) GetVClusterRancherCluster(ctx context.Context, serviceUID string) (unstructured.Unstructured, error) {
	clusterList := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{}}

	serviceHash := string(sha256.New().Sum([]byte(serviceUID))[:16])
	matchReq, err := labels.NewRequirement("loft.sh/vcluster-service-uid", selection.Equals, []string{serviceHash})
	if err != nil {
		return unstructured.Unstructured{}, err
	}

	clusterList.SetGroupVersionKind(schema.GroupVersionKind{Kind: "cluster", Group: "management.cattle.io", Version: "v3"})
	err = m.LocalClient.List(ctx, clusterList, &client.ListOptions{LabelSelector: labels.NewSelector().Add(*matchReq)})
	if err != nil && !errors.IsNotFound(err) {
		return unstructured.Unstructured{}, err
	}

	if len(clusterList.Items) == 0 {
		return unstructured.Unstructured{}, nil
	}
	return clusterList.Items[0], nil
}

func (m *Manager) EnsureVClusterImported(ctx context.Context, clusterClient client.Client, vClusterIP, serviceUID, clusterName, serviceNS, serviceName string) (unstructured.Unstructured, error) {
	provisioningCluster := unstructured.Unstructured{Object: map[string]interface{}{"kind": "cluster", "apiVersion": "provisioning.cattle.io/v1"}}
	provisioningCluster.SetNamespace("fleet-default")
	provisioningClusterName := fmt.Sprintf("%s-%s-%s", clusterName, serviceNS, serviceName)

	provisioningCluster.SetName(provisioningClusterName)
	err := m.LocalClient.Create(ctx, &provisioningCluster)
	if err != nil {
		return unstructured.Unstructured{}, err
	}

	err = m.LocalClient.Get(ctx, client.ObjectKey{Name: provisioningClusterName, Namespace: "fleet-default"}, &provisioningCluster)
	if err != nil {
		return unstructured.Unstructured{}, err
	}
	return provisioningCluster, nil
}

func (m *Manager) EnsureRancherClusterAgentDeployed() {

}
