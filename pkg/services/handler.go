package services

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/cenkalti/backoff/v4"
	"github.com/loft-sh/vcluster-rancher-op/pkg/unstructured"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1unstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"sync"
)

type Handler struct {
	Ctx context.Context

	LocalClient unstructured.Client

	Lock          sync.RWMutex
	status        importStatus
	ClusterClient unstructured.Client
	ClusterName   string
}

var _ cache.ResourceEventHandler = (*Handler)(nil)

type importStatus string

const (
	success importStatus = "success"
	failed  importStatus = "failed"
)

func (h *Handler) OnAdd(obj interface{}, isInInitialList bool) {
	var status importStatus
	h.Lock.RLock()
	status = h.status
	defer h.Lock.RUnlock()
	if status == success {
		fmt.Println("vCluster has already been successfully imported")
		return
	}
	if status == failed {
		fmt.Println("vCluster previously failed to import, skipping.")
	}

	fmt.Println("on add service")
	service, ok := obj.(*corev1.Service)
	if !ok {
		fmt.Println("Issue with service")
		return
	}

	if service.Spec.ClusterIP == "None" {
		// skip headless service
		return
	}

	fmt.Println("Found service ", service.Name)
	serviceHash := string(sha256.New().Sum([]byte(service.GetUID()))[:16])
	_, err := h.LocalClient.GetFirstWithLabel(h.Ctx, schema.GroupVersionKind{Group: "provisioning.cattle.io", Version: "v1", Kind: "Cluster"}, "loft.sh/vcluster-service-uid", serviceHash)
	if err != nil && !errors.IsNotFound(err) {
		fmt.Println("Issue with getting cluster", err)
		return
	}

	if errors.IsNotFound(err) {
		fmt.Println("Not found!")
		p, err := h.LocalClient.Create(h.Ctx, schema.GroupVersionKind{Group: "provisioning.cattle.io", Version: "v1", Kind: "Cluster"}, fmt.Sprintf("%s-%s-%s", h.ClusterName, service.Namespace, service.Name), "fleet-default", map[string]string{"loft.sh/vcluster-service-uid": serviceHash}, nil)
		if err != nil && !errors.IsAlreadyExists(err) {
			fmt.Println("Issue creating cluster", err)
			return
		}
		fmt.Println("Cluster created!", p)
	}

	var managementCluster v1unstructured.Unstructured
	err = backoff.Retry(
		// the use of a backoff retry here is justifiable because we are not using a normal framework that will retry on error on our behalf.
		func() error {
			managementCluster, err = h.LocalClient.GetFirstWithLabel(h.Ctx, schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "Cluster"}, "loft.sh/vcluster-service-uid", serviceHash)
			if err != nil {
				fmt.Println("failed getting management cluster")
				return err
			}
			fmt.Println("successfully retrieve management cluster!", managementCluster)
			return nil
		}, backoff.NewExponentialBackOff())
	if err != nil {
		h.Lock.Lock()
		fmt.Println("clusters.management.cattle.io was never created by rancher. Exiting indefinitely.")
		return
	}

	clusterRegistrationToken, err := h.LocalClient.Get(h.Ctx, schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "ClusterRegistrationToken"}, "default-token", managementCluster.GetName())
	if err != nil {
		fmt.Println("failed to get clusterRegistrationToken", err)
		return
	}
	fmt.Println(clusterRegistrationToken)
	command := getNestedString(clusterRegistrationToken.Object, "Status", "InsecureCommand")
	fmt.Println("got command!", command)

	job := batchv1.Job{
		ObjectMeta: v1.ObjectMeta{
			Name:      "rancher-cluster-agent-install",
			Namespace: service.Namespace,
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					Name:      "rancher-cluster-agent-install-job",
					Namespace: service.Namespace,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "container-0",
						}
					},
				},
			},
		},
	}
	h.ClusterClient.Client.Create(h.Ctx, &job)
}

func (h *Handler) OnUpdate(oldObj, newObj interface{}) {
	fmt.Println("updated", oldObj, newObj)
}

func (h *Handler) OnDelete(obj interface{}) {
	fmt.Println("deleted", obj)
}

func getNestedString(obj map[string]interface{}, keys ...string) string {
	if len(keys) == 0 {
		return ""
	}

	if v, ok := obj[keys[0]].(string); ok {
		return v
	}

	if v, ok := obj[keys[0]].(map[string]interface{}); ok && len(keys) > 1 {
		getNestedString(v, keys[1:]...)
	}
	return ""
}
