package services

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/config"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/constants"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/queue"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/unstructured"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/unstructured/gvk"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1unstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	ErrRancherResourcesPending   = errors.New("rancher resources pending")
	ErrAgentInstallJobInProgress = errors.New("agent install job in progress")

	// we always exclude labels with our label prefixes. Otherwise, the labels this operator depend on will be removed causing it to malfunction.
	// helm labels are also excluded to prevent confusion. Most other labels will be user supplied or at least indirectly user controlled.
	labelKeyPrefixesToPreventFromSyncing = []string{"loft.sh/", "vcluster.loft.sh/", "app.kubernetes.io/"}
	labelKeysToPreventFromSyncing        = []string{"helm", "app", "heritage", "release"}
)

// Handler reconciles vCluster services by managing their Rancher cluster resources
type Handler struct {
	Ctx context.Context

	Logger logr.Logger

	LocalUnstructuredClient unstructured.Client

	Lock                      sync.RWMutex
	ClusterUnstructuredClient unstructured.Client
	ClusterClient             *kubernetes.Clientset
	ClusterName               string
	Config                    config.Config

	Indexer cache.Indexer
}

var _ queue.Reconciler = (*Handler)(nil)

// Reconcile processes a single queue item by looking up the service and reconciling it
func (h *Handler) Reconcile(_ context.Context, item queue.Item) error {
	// try to get the object from the indexer
	obj, exists, err := h.Indexer.GetByKey(item.Key)
	if err != nil {
		return fmt.Errorf("error fetching object with key %s from store: %w", item.Key, err)
	}

	if !exists {
		// object doesn't exist in cache - handle cleanup
		h.Logger.Info("service not found in cache, cleaning up", "key", item.Key)

		if item.UID == "" {
			h.Logger.Info("no UID available for deleted service, cannot clean up", "key", item.Key)
			return nil
		}

		return h.cleanupProvisioningCluster(item.UID, item.Key)
	}

	// object exists - check if it's being deleted
	service := obj.(*corev1.Service)
	if !service.DeletionTimestamp.IsZero() {
		// being deleted - clean up
		h.Logger.Info("service has deletionTimestamp set, cleaning up", "key", item.Key)
		return h.cleanupProvisioningCluster(string(service.UID), item.Key)
	}

	// normal add/update reconciliation
	return h.deployvClusterRancherCluster(service)
}

func (h *Handler) cleanupProvisioningCluster(uid, key string) error {
	provisioningCluster, err := h.LocalUnstructuredClient.GetFirstWithLabel(h.Ctx, gvk.ClusterProvisioningCattle, constants.LabelVClusterServiceUID, uid)
	if err != nil {
		if kerrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("error getting provisioning cluster for vCluster instance %s: %w", key, err)
	}

	// check if this provisioning cluster belongs to this host cluster
	if provisioningCluster.GetLabels()[constants.LabelHostClusterName] != h.ClusterName {
		return nil
	}

	err = h.LocalUnstructuredClient.Delete(h.Ctx, &provisioningCluster)
	if err != nil {
		return fmt.Errorf("failed to delete provisioning cluster for vCluster instance %s: %w", key, err)
	}

	h.Logger.Info("successfully deleted provisioning cluster", "key", key, "uid", uid)
	return nil
}

func (h *Handler) ensureProvisioningCluster(logger logr.Logger, service *corev1.Service, ns *corev1.Namespace) (v1unstructured.Unstructured, error) {
	provisioningCluster, err := h.LocalUnstructuredClient.GetFirstWithLabel(h.Ctx, gvk.ClusterProvisioningCattle, constants.LabelVClusterServiceUID, string(service.GetUID()))
	if client.IgnoreNotFound(err) != nil {
		return v1unstructured.Unstructured{}, fmt.Errorf("error getting provisioning cluster for vCluster instance: %w", err)
	}

	if kerrors.IsNotFound(err) {
		logger.Info("provisioning cluster does not exist for vCluster instance, creating...")

		labels := map[string]string{
			constants.LabelVClusterServiceUID: string(service.GetUID()),
			constants.LabelHostClusterName:    h.ClusterName,
		}

		projectID, hasProjectID := ns.GetLabels()["field.cattle.io/projectId"]
		if hasProjectID {
			project, err := h.LocalUnstructuredClient.Get(h.Ctx, gvk.ProjectManagementCattle, projectID, h.ClusterName)
			if err != nil {
				return v1unstructured.Unstructured{}, fmt.Errorf("failed to get project for vCluster's namespace [%s]: %w", service.Namespace, err)
			}

			labels[constants.LabelProjectUID] = string(project.GetUID())
			labels[constants.LabelProjectName] = project.GetName()
		} else {
			labels[constants.LabelProjectUID] = constants.ValueNoRancherProjectOnNameSpace
		}

		clusterName, useGenName, err := h.getProvisioningClusterName(service)
		if err != nil {
			return v1unstructured.Unstructured{}, fmt.Errorf("failed to generate provisioning cluster name: %w", err)
		}
		provisioningCluster, err = h.LocalUnstructuredClient.Create(
			h.Ctx,
			gvk.ClusterProvisioningCattle,
			clusterName,
			h.getTargetClusterNamespace(labels[constants.LabelProjectUID]),
			useGenName,
			labels,
			nil,
			nil)
		if err != nil && !kerrors.IsAlreadyExists(err) {
			return v1unstructured.Unstructured{}, fmt.Errorf("errors creating provisioning cluster for vCluster instance: %w", err)
		}
		if err != nil {
			logger.Info("error from provisioning cluster creation: " + err.Error())
		}
	}

	logger.Info("supposedly found provisioning cluster " + provisioningCluster.GetName() + " " + string(service.UID) + " " + service.Namespace + " " + service.Name)
	return provisioningCluster, nil
}

func (h *Handler) getRancherResources(logger logr.Logger, service *corev1.Service, provisioningCluster v1unstructured.Unstructured) (isReady bool, managementCluster v1unstructured.Unstructured, clusterRegistrationToken v1unstructured.Unstructured, err error) {
	managementCluster, err = h.LocalUnstructuredClient.GetFirstWithLabel(h.Ctx, gvk.ClustersManagementCattle, constants.LabelVClusterServiceUID, string(service.GetUID()))
	if err != nil {
		if kerrors.IsNotFound(err) {
			logger.Info(fmt.Sprintf("waiting for rancher to create management cluster for vCluster %q: %q", service.Name, service.GetUID()))
			return false, v1unstructured.Unstructured{}, v1unstructured.Unstructured{}, ErrRancherResourcesPending
		}
		return false, v1unstructured.Unstructured{}, v1unstructured.Unstructured{}, fmt.Errorf("failed to get management cluster for vCluster: %w", err)
	}
	logger.Info("successfully retrieved management cluster for vCluster")

	orig := managementCluster.DeepCopy()

	logger.Info("syncing labels")
	labels := managementCluster.GetLabels()
	h.syncLabels(service.Labels, labels)
	managementCluster.SetLabels(labels)

	if unstructured.GetNested[bool](provisioningCluster.Object, "status", "ready") {
		if err := h.LocalUnstructuredClient.Patch(h.Ctx, &managementCluster, client.MergeFrom(orig)); err != nil {
			if kerrors.IsConflict(err) {
				return false, v1unstructured.Unstructured{}, v1unstructured.Unstructured{}, ErrRancherResourcesPending
			}
			return false, v1unstructured.Unstructured{}, v1unstructured.Unstructured{}, fmt.Errorf("failed to patch ready management cluster: %w", err)
		}
		return true, managementCluster, v1unstructured.Unstructured{}, nil
	}

	// if the annotation is set and the finalizer is not present, add it along with the target app label.
	// Otherwise, remove the finalizer
	if service.GetAnnotations()[constants.AnnotationUninstallOnDelete] == "true" {
		managementCluster.SetFinalizers(append(managementCluster.GetFinalizers(), constants.FinalizerVClusterApp))

		// for some reason setting annotations on the management cluster before its admission webhook deploys stops the agent from installing, so a label is used here
		labels := managementCluster.GetLabels()
		labels[constants.LabelTargetApp] = fmt.Sprintf("%s_%s", service.Annotations["meta.helm.sh/release-namespace"], service.Annotations["meta.helm.sh/release-name"])
		managementCluster.SetLabels(labels)
	} else if idx := slices.Index(managementCluster.GetFinalizers(), constants.FinalizerVClusterApp); idx >= 0 {
		managementCluster.SetFinalizers(slices.Delete(managementCluster.GetFinalizers(), idx, idx+1))
	}

	if err := h.LocalUnstructuredClient.Patch(h.Ctx, &managementCluster, client.MergeFrom(orig)); err != nil {
		if kerrors.IsConflict(err) {
			return false, v1unstructured.Unstructured{}, v1unstructured.Unstructured{}, ErrRancherResourcesPending
		}
		return false, v1unstructured.Unstructured{}, v1unstructured.Unstructured{}, fmt.Errorf("failed to patch pending management cluster: %w", err)
	}

	logger.Info("waiting for rancher to create cluster registration token for vCluster")
	clusterRegistrationToken, err = h.LocalUnstructuredClient.Get(h.Ctx, gvk.ClusterRegistrationTokenManagementCattle, "default-token", managementCluster.GetName())
	if err != nil {
		if kerrors.IsNotFound(err) {
			return false, managementCluster, v1unstructured.Unstructured{}, ErrRancherResourcesPending
		}
		return false, managementCluster, v1unstructured.Unstructured{}, fmt.Errorf("failed to get cluster registration token for vCluster: %w", err)
	}
	logger.Info("successfully retrieved cluster registration token for vCluster")

	_, err = h.ClusterClient.CoreV1().Secrets(service.Namespace).Get(h.Ctx, "vc-"+service.Name, v1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			return false, managementCluster, clusterRegistrationToken, ErrRancherResourcesPending
		}
		return false, managementCluster, clusterRegistrationToken, fmt.Errorf("failed to get vcluster secret: %w", err)
	}
	logger.Info("successfully confirmed vCluster secret was created")

	return true, managementCluster, clusterRegistrationToken, nil
}

func (h *Handler) ensureAgentDeployed(logger logr.Logger, service *corev1.Service, clusterRegistrationToken v1unstructured.Unstructured, isReady bool) error {
	pods, err := h.ClusterClient.CoreV1().Pods(service.Namespace).List(h.Ctx, v1.ListOptions{LabelSelector: "app=cattle-cluster-agent"})
	if err != nil {
		return fmt.Errorf("failed listing rancher cluster agent pods: %w", err)
	}

	if len(pods.Items) > 0 {
		logger.Info("a rancher cluster agent pod already exists in namespace, skipping")
		return nil
	}

	if isReady {
		logger.Info("provisioning cluster is ready but no agent pod found, will deploy agent")
	}

	logger.Info("checking for existing rancher cluster agent install jobs")
	jobs, err := h.ClusterClient.BatchV1().Jobs(service.Namespace).List(h.Ctx, v1.ListOptions{LabelSelector: "vcluster.loft.sh/install-job=true"})
	if err != nil {
		return fmt.Errorf("failed listing rancher cluster agent install jobs: %w", err)
	}

	for _, j := range jobs.Items {
		if j.Status.Failed > 0 {
			logger.Info("rancher cluster agent install job failed, deleting it to retry", "jobName", j.Name)
			err = h.ClusterClient.BatchV1().Jobs(service.Namespace).Delete(h.Ctx, j.Name, v1.DeleteOptions{})
			if err != nil {
				return fmt.Errorf("failed to delete failed rancher cluster agent install job: %w", err)
			}
			continue
		} else if j.Status.Active > 0 {
			logger.Info("rancher cluster agent install job is still active", "jobName", j.Name)
			return ErrAgentInstallJobInProgress
		} else if j.Status.Succeeded > 0 {
			logger.Info("rancher cluster agent install job succeeded, skipping", "jobName", j.Name)
			return nil
		}
	}

	command := unstructured.GetNested[string](clusterRegistrationToken.Object, "status", "insecureCommand")
	deleteTTL := int32(0)

	logger.Info("creating job to deploy rancher cluster agent in vCluster")
	job := batchv1.Job{
		ObjectMeta: v1.ObjectMeta{
			GenerateName: "rancher-agent-install-",
			Namespace:    service.Namespace,
			Labels: map[string]string{
				"vcluster.loft.sh/install-job": "true",
			},
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

func (h *Handler) deployvClusterRancherCluster(obj interface{}) error {
	service, ok := obj.(*corev1.Service)
	if !ok {
		return errors.New("object is not a service, skipping")
	}

	logger := h.Logger.WithValues("vclusterName", service.Name, "vclusterNamespace", service.Namespace, "hostCluster", h.ClusterName, "action", "add")

	if service.Spec.ClusterIP == "None" {
		// skip headless service
		return nil
	}

	if service.Annotations[constants.AnnotationSkipImport] == "true" {
		logger.Info(fmt.Sprintf("service annotation %q set to \"true\". Skipping import.", constants.AnnotationSkipImport))
		return nil
	}

	ns, err := h.ClusterClient.CoreV1().Namespaces().Get(h.Ctx, service.Namespace, v1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get vCluster's namespace [%s]: %w", service.Namespace, err)
	}

	provisioningCluster, err := h.ensureProvisioningCluster(logger, service, ns)
	if err != nil {
		return err
	}

	isReady, _, clusterRegistrationToken, err := h.getRancherResources(logger, service, provisioningCluster)
	if err != nil {
		return fmt.Errorf("failed waiting for rancher to create resource(s) for vCluster: %w", err)
	}

	return h.ensureAgentDeployed(logger, service, clusterRegistrationToken, isReady)
}

func (h *Handler) syncLabels(from, to map[string]string) {
	keysToSync := make(map[string]struct{})
	for _, k := range h.Config.ServiceSyncIncludeLabelKeys {
		keysToSync[k] = struct{}{}
	}

	keys := sets.NewString(append(sets.StringKeySet(from).UnsortedList(), sets.StringKeySet(to).UnsortedList()...)...)

	for _, k := range keys.List() {
		if slices.ContainsFunc[[]string, string](h.Config.ServiceSyncIncludeLabelKeysWithPrefix, func(s string) bool {
			return strings.HasPrefix(k, s)
		}) {
			keysToSync[k] = struct{}{}
		}
	}

	keysToExclude := sets.NewString(append(labelKeysToPreventFromSyncing, h.Config.ServiceSyncExcludeLabelKeys...)...).List()
	for _, k := range keysToExclude {
		delete(keysToSync, k)
	}

	keyPrefixesToExclude := sets.NewString(append(labelKeyPrefixesToPreventFromSyncing, h.Config.ServiceSyncExcludeLabelKeysWithPrefix...)...).List()
	for k := range keysToSync {
		if slices.ContainsFunc[[]string, string](keyPrefixesToExclude, func(s string) bool {
			return strings.HasPrefix(k, s)
		}) {
			continue
		}

		if from[k] == "" {
			delete(to, k)
			continue
		}
		to[k] = from[k]
	}
}

func (h *Handler) getProvisioningClusterName(service *corev1.Service) (string, bool, error) {
	// Check for custom name annotation
	if customName, ok := service.Annotations[constants.AnnotationCustomName]; ok && customName != "" {
		if len(customName) > 63 {
			return "", false, fmt.Errorf("custom name %q exceeds 63 character limit", customName)
		}
		return customName, false, nil
	}

	// Check for custom name prefix annotation
	if customPrefix, ok := service.Annotations[constants.AnnotationCustomNamePrefix]; ok && customPrefix != "" {
		hasHyphen := strings.HasSuffix(customPrefix, "-")
		maxLen := 57
		if hasHyphen {
			maxLen = 58
		}
		if len(customPrefix) > maxLen {
			return "", false, fmt.Errorf("custom name prefix %q exceeds %d character limit (must leave room for hyphen and 5 generated chars)", customPrefix, maxLen)
		}
		if !hasHyphen {
			customPrefix += "-"
		}
		return customPrefix, true, nil
	}

	// Use default name format
	name := fmt.Sprintf("%s-%s-%s", h.ClusterName, service.Namespace, service.Name)
	if len(name) <= 63 {
		return name, false, nil
	}

	// Default name is too long, use it as generateName
	// Trim to 57 chars to leave room for the trailing hyphen and 5 chars added by generateName
	if len(name) > 57 {
		name = name[:57]
	}
	if !strings.HasSuffix(name, "-") {
		name += "-"
	}

	return name, true, nil
}

func (h *Handler) getTargetClusterNamespace(projectUID string) string {
	if projectUID != "" && h.Config.FleetProjectUIDToWorkspaceMappings[projectUID] != "" {
		return h.Config.FleetProjectUIDToWorkspaceMappings[projectUID]
	}
	return h.Config.FleetDefaultWorkspace
}
