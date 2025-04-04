package services

import (
	"context"
	errors2 "errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/unstructured"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1unstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type Handler struct {
	Ctx context.Context

	Logger logr.Logger

	LocalUnstructuredClient unstructured.Client

	Lock                      sync.RWMutex
	ClusterUnstructuredClient unstructured.Client
	ClusterClient             *kubernetes.Clientset
	ClusterName               string
}

var _ cache.ResourceEventHandler = (*Handler)(nil)

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

	logger := h.Logger.WithValues("vclusterName", service.Name, "vclusterNamespace", service.Namespace, "hostCluster", h.ClusterName, "action", "add")

	if service.Spec.ClusterIP == "None" {
		// skip headless service
		return nil
	}

	if service.Annotations["loft.sh/vcluster-skip-import"] == "true" {
		logger.Info("service annotation \"loft.sh/vcluster-skip-import\" set to \"true\". Skipping import.")
		return nil
	}

	ns, err := h.ClusterClient.CoreV1().Namespaces().Get(h.Ctx, service.Namespace, v1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get vCluster's namespace [%s]: %w", service.Namespace, err)
	}

	pods, err := h.ClusterClient.CoreV1().Pods(service.Namespace).List(h.Ctx, v1.ListOptions{LabelSelector: "app=cattle-cluster-agent"})
	if err != nil {
		return fmt.Errorf("failed listing rancher cluster agent pods: %w", err)
	}

	if len(pods.Items) > 0 {
		logger.Info("a rancher cluster agent pod already exists in namespace, skipping")
		return nil
	}

	project, err := h.LocalUnstructuredClient.Get(h.Ctx, schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "Project"}, ns.GetLabels()["field.cattle.io/projectId"], h.ClusterName)
	if err != nil {
		return fmt.Errorf("failed to get project for vCluster's namespace [%s]: %w", service.Namespace, err)
	}

	provisioningCluster, err := h.LocalUnstructuredClient.GetFirstWithLabel(h.Ctx, schema.GroupVersionKind{Group: "provisioning.cattle.io", Version: "v1", Kind: "Cluster"}, "loft.sh/vcluster-service-uid", string(service.GetUID()))
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("error getting provisioning cluster for vCluster instance: %w", err)
	}

	if errors.IsNotFound(err) {
		logger.Info("provisioning cluster does not exist for vCluster instance, creating...")
		provisioningCluster, err = h.LocalUnstructuredClient.Create(
			h.Ctx,
			schema.GroupVersionKind{Group: "provisioning.cattle.io", Version: "v1", Kind: "Cluster"},
			fmt.Sprintf("%s-%s-%s", h.ClusterName, service.Namespace, service.Name),
			"fleet-default", false,
			map[string]string{
				"loft.sh/vcluster-service-uid":  string(service.GetUID()),
				"loft.sh/vcluster-project-uid":  string(project.GetUID()),
				"loft.sh/vcluster-project":      project.GetName(),
				"loft.sh/vcluster-host-cluster": h.ClusterName,
			},
			nil,
			nil)
		if err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("errors creating provisioning cluster for vCluster instance: %w", err)
		}
	}

	if unstructured.GetNested[bool](provisioningCluster.Object, "status", "ready") {
		logger.Info("agent has already been deployed for vCluster, skipping")
		return nil
	}

	var managementCluster, clusterRegistrationToken v1unstructured.Unstructured
	err = wait.ExponentialBackoff(wait.Backoff{Duration: 100 * time.Millisecond, Steps: 20, Factor: 1.5},
		// the use of a backoff retry here is justifiable because we are not using a normal framework that will retry on error on our behalf.
		func() (bool, error) {
			logger.Info("waiting for rancher to create management cluster for vCluster")
			managementCluster, err = h.LocalUnstructuredClient.GetFirstWithLabel(h.Ctx, schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "Cluster"}, "loft.sh/vcluster-service-uid", string(service.GetUID()))
			if err != nil {
				if errors.IsNotFound(err) {
					return false, nil
				}
				return false, fmt.Errorf("failed to get management cluster for vCluster: %w", err)
			}
			logger.Info("successfully retrieved management cluster for vCluster")

			if service.GetAnnotations()["loft.sh/uninstall-on-cluster-delete"] == "true" {
				managementCluster.SetFinalizers([]string{"loft.sh/vcluster-app-cleanup"})
				// for some reason setting annotations on the management cluster before its admission webhook deploys stops the agent from installing, so a label is used here
				managementCluster.SetLabels(map[string]string{"loft.sh/target-app": fmt.Sprintf("%s_%s", service.Annotations["meta.helm.sh/release-namespace"], service.Annotations["meta.helm.sh/release-name"])})
				err = h.LocalUnstructuredClient.Update(h.Ctx, &managementCluster)
				if err != nil {
					if errors.IsConflict(err) {
						return false, nil
					}
					return false, fmt.Errorf("failed to add ap cleanup finalizer \"loft.sh/uninstall-on-cluster-delete\" to management cluster: %w", err)
				}
			}

			logger.Info("waiting for rancher to create cluster registration token for vCluster")
			clusterRegistrationToken, err = h.LocalUnstructuredClient.Get(h.Ctx, schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "ClusterRegistrationToken"}, "default-token", managementCluster.GetName())
			if err != nil {
				if errors.IsNotFound(err) {
					return false, nil
				}
				return false, fmt.Errorf("failed to get cluster registration token for vCluster: %w", err)
			}
			logger.Info("successfully retrieved cluster registration token for vCluster")

			_, err = h.ClusterClient.CoreV1().Secrets(service.Namespace).Get(h.Ctx, "vc-"+service.Name, v1.GetOptions{})
			if err != nil {
				if errors.IsNotFound(err) {
					return false, nil
				}
				return false, fmt.Errorf("failed to get vcluster secret: %w", err)
			}
			logger.Info("successfully confirmed vCluster secret was created")

			return true, nil
		})
	if err != nil {
		return fmt.Errorf("failed waiting for rancher to create resource(s) for vCluster: %w", err)
	}

	command := unstructured.GetNested[string](clusterRegistrationToken.Object, "status", "insecureCommand")
	deleteTTL := int32(0)

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
			TTLSecondsAfterFinished: &deleteTTL,
		},
	}
	err = h.ClusterUnstructuredClient.Client.Create(h.Ctx, &job)
	if err != nil {
		return fmt.Errorf("failed to create job for deploying rancher agent in vCluster: %w", err)
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

	provisioningCluster, err := h.LocalUnstructuredClient.GetFirstWithLabel(h.Ctx, schema.GroupVersionKind{Group: "provisioning.cattle.io", Version: "v1", Kind: "Cluster"}, "loft.sh/vcluster-service-uid", string(service.GetUID()))
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("error getting provisioning cluster for vCluster instance: %w", err)
	}

	err = h.LocalUnstructuredClient.Delete(h.Ctx, &provisioningCluster)
	if err != nil {
		return fmt.Errorf("failed to delete provisioning cluster for vCluster in cluster: %w", err)
	}

	return nil
}
