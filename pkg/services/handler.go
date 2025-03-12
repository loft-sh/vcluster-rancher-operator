package services

import (
	"context"
	"crypto/sha256"
	errors2 "errors"
	"fmt"
	"strings"
	"sync"

	"github.com/cenkalti/backoff/v4"
	"github.com/go-logr/logr"
	"github.com/loft-sh/vcluster-rancher-op/pkg/unstructured"
	errors3 "github.com/onsi/gomega/gstruct/errors"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1unstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type Handler struct {
	Ctx context.Context

	Logger logr.Logger

	LocalUnstructuredClient unstructured.Client

	Lock                      sync.RWMutex
	status                    importStatus
	ClusterUnstructuredClient unstructured.Client
	ClusterClient             *kubernetes.Clientset
	ClusterName               string
}

var _ cache.ResourceEventHandler = (*Handler)(nil)

type importStatus string

func (h *Handler) OnAdd(obj interface{}, isInInitialList bool) {
	err := h.deployvClusterRancherCluster(obj)
	if err != nil {
		h.Logger.Error(err, "failed to deploy rancher cluster for vCluster")
	}
}

func (h *Handler) OnUpdate(_, _ interface{}) {
}

func (h *Handler) OnDelete(obj interface{}) {
	err := h.deletevClusterRancherCluster(obj)
	if err != nil {
		h.Logger.Error(err, "failed to deploy rancher cluster for vCluster")
	}
}

func (h *Handler) deployvClusterRancherCluster(obj interface{}) error {
	service, ok := obj.(*corev1.Service)
	if !ok {
		return errors2.New("object is not a service, skipping")
	}

	logger := h.Logger.WithValues("vclusterName", service.Name, "hostCluster", h.ClusterName, "action", "add")

	if service.Spec.ClusterIP == "None" {
		// skip headless service
		return nil
	}

	serviceHash := string(sha256.New().Sum([]byte(service.GetUID()))[:16])
	provisioningCluster, err := h.LocalUnstructuredClient.GetFirstWithLabel(h.Ctx, schema.GroupVersionKind{Group: "provisioning.cattle.io", Version: "v1", Kind: "Cluster"}, "loft.sh/vcluster-service-uid", serviceHash)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("error getting provisioning cluster for vCluster instance: %w", err)
	}

	if errors.IsNotFound(err) {
		logger.Info("provisioning cluster does not exist for vCluster instance, creating...")
		provisioningCluster, err = h.LocalUnstructuredClient.Create(h.Ctx, schema.GroupVersionKind{Group: "provisioning.cattle.io", Version: "v1", Kind: "Cluster"}, fmt.Sprintf("%s-%s-%s", h.ClusterName, service.Namespace, service.Name), "fleet-default", false, map[string]string{"loft.sh/vcluster-service-uid": serviceHash}, nil)
		if err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("errors creating provisioning cluster for vCluster instance: %w", err)
		}
	}

	if getNested[bool](provisioningCluster.Object, "status", "ready") {
		logger.Info("agent has already been deployed for vCluster, skipping")
		return nil
	}

	var managementCluster, clusterRegistrationToken v1unstructured.Unstructured
	err = backoff.Retry(
		// the use of a backoff retry here is justifiable because we are not using a normal framework that will retry on error on our behalf.
		func() error {
			logger.Info("waiting for rancher to create management cluster for vCluster")
			managementCluster, err = h.LocalUnstructuredClient.GetFirstWithLabel(h.Ctx, schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "Cluster"}, "loft.sh/vcluster-service-uid", serviceHash)
			if err != nil {
				return fmt.Errorf("failed to get management cluster for vCluster: %w", err)
			}
			logger.Info("successfully retrieved management cluster for vCluster")

			logger.Info("waiting for rancher to create cluster registration token for vCluster")
			clusterRegistrationToken, err = h.LocalUnstructuredClient.Get(h.Ctx, schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "ClusterRegistrationToken"}, "default-token", managementCluster.GetName())
			if err != nil {
				return fmt.Errorf("failed to get cluster registration token for vCluster: %w", err)
			}
			logger.Info("successfully retrieved cluster registration token for vCluster")
			return nil
		}, backoff.NewExponentialBackOff())
	if err != nil {
		return fmt.Errorf("failed waiting for rancher to create resource(s) for vCluster: %w", err)
	}

	command := getNested[string](clusterRegistrationToken.Object, "status", "insecureCommand")

	logger.Info("creating job to deploy rancher cluster agent in vCluster")
	job := batchv1.Job{
		ObjectMeta: v1.ObjectMeta{
			GenerateName: "rancher-agent-install-",
			Namespace:    service.Namespace,
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
							Name:            "container-0",
							Image:           "alpine/k8s:1.30.9",
							ImagePullPolicy: "Always",
							Command:         []string{"/bin/bash"},
							Args: []string{
								"-c",
								fmt.Sprintf(`cat /etc/config/config | sed 's/localhost:8443/%s:443/g' > /tmp/vcluster-kc.yaml && %s`, service.Spec.ClusterIP, strings.Replace(command, "kubectl ", "kubectl --kubeconfig /tmp/vcluster-kc.yaml ", 1)),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "kubeconfig",
									MountPath: "/etc/config",
								},
							},
						},
					},
					ServiceAccountName: "vc-" + service.Name,
					RestartPolicy:      "Never",
					Volumes: []corev1.Volume{
						{
							Name: "kubeconfig",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "vc-" + service.Name,
								},
							},
						},
					},
				},
			},
		},
	}
	err = h.ClusterUnstructuredClient.Client.Create(h.Ctx, &job)
	if err != nil {
		return fmt.Errorf("failed to create job for deploying rancher agent in vCluster: %w", err)
	}

	ns, err := h.ClusterClient.CoreV1().Namespaces().Get(h.Ctx, service.Namespace, v1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get vCluster's namespace [%s]: %w", service.Namespace, err)
	}

	logger.Info(fmt.Sprintf("vCluster's project detected: %s", ns.GetLabels()["field.cattle.io/projectId"]))
	projectRoleTemplateBindings, err := h.LocalUnstructuredClient.List(h.Ctx, schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "ProjectRoleTemplateBinding"}, strings.TrimSuffix(ns.GetLabels()["field.cattle.io/projectId"], h.ClusterName+":"))
	if err != nil {
		return fmt.Errorf("failed to list projectRoleTemplateBindings that target vCluster's project [%s]: %w", ns.GetLabels()["field.cattle.io/projectId"], err)
	}

	projectRoleTemplateBindings = getMatchingItems[string](projectRoleTemplateBindings, fmt.Sprintf("%s:%s", h.ClusterName, ns.GetLabels()["field.cattle.io/projectId"]), "projectName")
	projectRoleTemplateBindings.Items = append(getMatchingItems[string](projectRoleTemplateBindings, "project-owner", "roleTemplateName").Items, getMatchingItems[string](projectRoleTemplateBindings, "project-member", "roleTemplateName").Items...)

	forEachErrors := forEachItem(
		projectRoleTemplateBindings,
		func(item v1unstructured.Unstructured) error {
			user := getNested[string](item.Object, "userName")
			if user == "" {
				return fmt.Errorf("user cannot be empty string")
			}

			_, err = h.LocalUnstructuredClient.Create(
				h.Ctx,
				schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "ClusterRoleTemplateBinding"},
				"vcluster-owner-",
				managementCluster.GetName(), true, nil,
				map[string]interface{}{
					"userName":         user,
					"clusterName":      managementCluster.GetName(),
					"roleTemplateName": "cluster-owner"})
			if err != nil {
				return fmt.Errorf("failed to create cluster role template binding to add user [%s] as a cluster owner to the vCluster's rancher cluster [%s]: %w", user, managementCluster.GetName(), err)
			}
			return nil
		})
	for _, err := range forEachErrors {
		fmt.Printf("Erros create prtb: %v\n", err)
	}
	if len(forEachErrors) > 0 {
		return fmt.Errorf("failed to to create %d cluster role template bindings for vCluster's rancher cluster: %w", len(forEachErrors), errors3.AggregateError(forEachErrors))
	}

	logger.Info("successfully deployed cluster for vCluster. Exiting...")

	return nil
}

func (h *Handler) deletevClusterRancherCluster(obj interface{}) error {
	service, ok := obj.(*corev1.Service)
	if !ok {
		return errors2.New("object is not a service, skipping")
	}

	logger := h.Logger.WithValues("vclusterName", service.Name, "hostCluster", h.ClusterName, "action", "delete")
	if service.Spec.ClusterIP == "None" {
		// skip headless service
		return nil
	}

	logger.Info("deleting vCluster's rancher cluster")

	serviceHash := string(sha256.New().Sum([]byte(service.GetUID()))[:16])
	provisioningCluster, err := h.LocalUnstructuredClient.GetFirstWithLabel(h.Ctx, schema.GroupVersionKind{Group: "provisioning.cattle.io", Version: "v1", Kind: "Cluster"}, "loft.sh/vcluster-service-uid", serviceHash)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("error getting provisioning cluster for vCluster instance: %w", err)
	}

	err = h.LocalUnstructuredClient.Delete(h.Ctx, &provisioningCluster)
	if err != nil {
		return fmt.Errorf("failed to delete provisioning cluster for vCluster in cluster: %w", err)
	}

	return nil
}

func getNested[T any](obj map[string]interface{}, keys ...string) T {
	var t T
	if obj == nil || len(keys) == 0 {
		return t
	}

	switch v := obj[keys[0]].(type) {
	case T:
		return v
	case map[string]interface{}:
		if len(keys) > 1 {
			return getNested[T](v, keys[1:]...)
		}
	}

	return t
}

func getMatchingItems[T comparable](list v1unstructured.UnstructuredList, filter T, keys ...string) v1unstructured.UnstructuredList {
	var matchingItems []v1unstructured.Unstructured
	for _, item := range list.Items {
		value := getNested[T](item.Object, keys...)
		if value != filter {
			continue
		}
		matchingItems = append(matchingItems, item)
	}
	list.Items = matchingItems
	return list
}

func forEachItem(list v1unstructured.UnstructuredList, doFunc func(item v1unstructured.Unstructured) error) []error {
	var doErrors []error
	for _, item := range list.Items {
		if err := doFunc(item); err != nil {
			doErrors = append(doErrors, err)
		}
	}
	return doErrors
}
