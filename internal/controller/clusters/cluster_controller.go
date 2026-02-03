/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package clusters

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/config"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/constants"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/queue"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/rancher"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/services"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/token"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/unstructured"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/unstructured/gvk"
	gerrors "github.com/onsi/gomega/gstruct/errors"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1unstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var httpClient = http.Client{
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	},
}

// ClusterReconciler reconciles a Cluster object
type ClusterReconciler struct {
	Client unstructured.Client
	Scheme *runtime.Scheme
	Config config.Config

	sync.Map
	lock         sync.RWMutex
	RancherToken string
	peers        map[string]context.CancelFunc
}

// +kubebuilder:rbac:groups=management.cattle.io,resources=clusters;tokens,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=management.cattle.io,resources=clusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=management.cattle.io,resources=clusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=management.cattle.io,resources=users,verbs=list
// +kubebuilder:rbac:groups=provisioning.cattle.io,resources=clusters;tokens,verbs=get;list;create;update;delete

// Reconcile
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	managementCluster, err := r.Client.Get(ctx, gvk.ClustersManagementCattle, req.Name, "")
	if err != nil {
		if kerrors.IsNotFound(err) {
			r.lock.Lock()
			if cancel, ok := r.peers[req.Name]; ok {
				logger.Info(fmt.Sprintf("stopping handler and informer for deleted cluster %s", req.Name))
				cancel()
				delete(r.peers, req.Name)
			}
			r.lock.Unlock()
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	staleErr := r.SyncStaleResources(ctx, logger, managementCluster)
	cleanupErr := r.SyncCleanup(ctx, logger, managementCluster)
	rbacErr := r.SyncRancherRBAC(ctx, logger, managementCluster)
	installErr := r.SyncvClusterInstallHandler(ctx, logger, managementCluster.GetName())
	metricsErr := r.SyncResourceLimits(ctx, logger, managementCluster)

	return ctrl.Result{}, errors.Join(staleErr, cleanupErr, rbacErr, installErr, metricsErr)
}

func (r *ClusterReconciler) SyncvClusterInstallHandler(ctx context.Context, logger logr.Logger, clusterName string) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	if _, ok := r.peers[clusterName]; ok {
		return nil
	}

	restConfig, err := token.RestConfigFromToken(clusterName, r.RancherToken)
	if err != nil {
		return err
	}

	clusterClient, unstructuredClusterClient, err := getClusterClient(restConfig)
	if err != nil {
		return err
	}

	sharedInformer := cache.NewSharedIndexInformer(&cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			options.LabelSelector = "app=vcluster"
			return clusterClient.CoreV1().Services("").List(ctx, options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			options.LabelSelector = "app=vcluster"
			return clusterClient.CoreV1().Services("").Watch(ctx, options)
		},
	}, &corev1.Service{}, time.Hour, cache.Indexers{})

	// create the reconciler handler
	handler := &services.Handler{
		Ctx:                       ctx,
		Logger:                    logger,
		LocalUnstructuredClient:   r.Client,
		ClusterUnstructuredClient: unstructuredClusterClient,
		ClusterClient:             clusterClient,
		ClusterName:               clusterName,
		Config:                    r.Config,
		Indexer:                   sharedInformer.GetIndexer(),
	}

	// wrap the handler with queue processing and concurrent workers.
	// We use a custom workqueue-based handler here instead of a controller-runtime Manager for each host cluster.
	// Managers are designed to be used on start up until program exit; they cannot be stopped simply and
	// often leave leaked goroutines. This approach also avoids the overhead of dozens of redundant HTTP
	// servers (metrics, health probes) for every cluster, making cleanup as simple as a single context cancellation.
	queuedHandler := queue.NewHandler(handler, sharedInformer.GetIndexer(), logger, 5, []error{services.ErrRancherResourcesPending, services.ErrAgentInstallJobInProgress})

	_, err = sharedInformer.AddEventHandler(queuedHandler)
	if err != nil {
		return err
	}

	clusterCtx, cancel := context.WithCancel(ctx)

	if r.peers == nil {
		r.peers = make(map[string]context.CancelFunc)
	}
	r.peers[clusterName] = cancel

	go func() {
		logger.Info(fmt.Sprintf("starting handler for %s's vclusters\n", clusterName))
		go queuedHandler.Start(clusterCtx)
		sharedInformer.Run(clusterCtx.Done())
		logger.Info(fmt.Sprintf("finished running handler for %s's vclusters\n", clusterName))
	}()
	return nil
}

func getClusterClient(restConfig *rest.Config) (*kubernetes.Clientset, unstructured.Client, error) {
	clusterClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, unstructured.Client{}, err
	}

	clusterConfig, err := cluster.New(restConfig)
	if err != nil {
		return nil, unstructured.Client{}, err
	}

	return clusterClient, unstructured.Client{Client: clusterConfig.GetClient()}, nil
}

// SyncRancherRBAC adds creates cluster owners in a vCluster's Rancher cluster based on ProjectRoleTemplateBindings (PRTBs) targeting the project it is installed in and
// ClusterRoleTemplateBindings (CRTBs) targeting the host rancher cluster it is installed in. Only RoleTemplateBindings (RTBs- this refers to CRTBs and PRTBs and is not an
// actual type) that have specific roleTemplate names will cause a user to be added as a cluster owner of the vCluster rancher cluster. The relevant PRTB roleTemplateNames
// are "project-member" and "project-owner". The relevant CRTB roleTemplateNames are "cluster-member" and "cluster-project". The PRTB or CRTB that led to the creation of
// the cluster owner CRTB is the CRTB's "parent-prtb" or "parent-crtb" respectively. Once the CRTBs parent is deleted, the CRTB will be deleted, removing the user as a
// cluster owner from the vCluster's Rancher cluster.
func (r *ClusterReconciler) SyncRancherRBAC(ctx context.Context, logger logr.Logger, managementCluster v1unstructured.Unstructured) error {
	projectUID := managementCluster.GetLabels()[constants.LabelProjectUID]
	projectName := managementCluster.GetLabels()[constants.LabelProjectName]
	hostClusterName := managementCluster.GetLabels()[constants.LabelHostClusterName]

	if projectUID == "" && projectName == "" && hostClusterName == "" {
		// not a vcluster management cluster
		return nil
	}

	if projectName == "" || projectUID == "" || hostClusterName == "" {
		if projectUID == constants.ValueNoRancherProjectOnNameSpace {
			return nil
		}
		logger.Info(fmt.Sprintf("vCluster management cluster %q missing at least 1 vCluster label(s)", managementCluster.GetName()))

		return nil
	}

	project, err := r.Client.Get(ctx, gvk.ProjectManagementCattle, projectName, hostClusterName)
	if err != nil {
		return fmt.Errorf("failed to get vCluster's project [%s/%s]: %w", hostClusterName, projectName, err)
	}

	if string(project.GetUID()) != projectUID {
		return fmt.Errorf("vCluster was installed in project [%[1]s] with UID [%[2]s]. Current project [%[1]s] has mismatched UID [%[3]s]", projectName, projectUID, project.GetUID())
	}

	prtbNamespace := project.GetName()
	if backingNamespace := unstructured.GetNested[string](project.Object, "status", "backingNamespace"); backingNamespace != "" {
		prtbNamespace = backingNamespace
	}

	projectRoleTemplateBindings, err := r.Client.List(ctx, gvk.ProjectRoleTemplateBindingManagementCattle, prtbNamespace)
	if err != nil {
		return fmt.Errorf("failed to list projectRoleTemplateBindings that target vCluster's project [%s] in namespace [%s]: %w", project.GetName(), prtbNamespace, err)
	}

	projectRoleTemplateBindings = unstructured.FilterItems[string](projectRoleTemplateBindings, fmt.Sprintf("%s:%s", project.GetNamespace(), project.GetName()), true, "projectName")
	projectRoleTemplateBindings.Items = append(unstructured.FilterItems[string](projectRoleTemplateBindings, "project-owner", true, "roleTemplateName").Items, unstructured.FilterItems[string](projectRoleTemplateBindings, "project-member", true, "roleTemplateName").Items...)

	requirement, err := labels.NewRequirement(constants.LabelVClusterServiceUID, selection.DoesNotExist, nil)
	if err != nil {
		return fmt.Errorf("failed to create requirement that filters for ClusterRoleTemplateBindings without loft.sh/vcluster-service-uid label: %w", err)
	}
	clusterRoleTemplateBindings, err := r.Client.ListWithOptions(ctx, gvk.ClusterRoleTemplateBindingManagementCattle, &client.ListOptions{Namespace: hostClusterName, LabelSelector: labels.NewSelector().Add(*requirement)})
	if err != nil {
		return fmt.Errorf("failed to list clusterRoleTemplateBindings that target vCluster's host cluster [%s]: %w", hostClusterName, err)
	}

	clusterRoleTemplateBindings = unstructured.FilterItems[string](clusterRoleTemplateBindings, "cluster-owner", true, "roleTemplateName")

	roleTemplateBindings := v1unstructured.UnstructuredList{Items: append(projectRoleTemplateBindings.Items, clusterRoleTemplateBindings.Items...)}

	clusterOwners := make(map[string]struct{})
	forEachErrors := unstructured.ForEachItem(
		roleTemplateBindings,
		func(_ int, item v1unstructured.Unstructured) error {
			user := unstructured.GetNested[string](item.Object, "userName")
			if user == "" {
				return fmt.Errorf("user cannot be empty string")
			}

			clusterOwners[user] = struct{}{}

			_, err := r.Client.Get(ctx, gvk.ClusterRoleTemplateBindingManagementCattle, fmt.Sprintf("vcluster-%s-co", user), managementCluster.GetName())
			if err != nil && !kerrors.IsNotFound(err) {
				return err
			}

			if err == nil {
				return nil
			}

			parentLabel := constants.LabelParentRoleTemplateBinding
			if item.GetKind() == "ClusterRoleTemplateBinding" {
				parentLabel = constants.LabelChildRoleTemplateBinding
			}

			_, err = r.Client.Create(
				ctx,
				gvk.ClusterRoleTemplateBindingManagementCattle,
				fmt.Sprintf("vcluster-%s-co", user),
				managementCluster.GetName(), false,
				map[string]string{
					constants.LabelVClusterServiceUID: managementCluster.GetLabels()[constants.LabelVClusterServiceUID],
					parentLabel:                       item.GetName(),
				},
				nil,
				map[string]any{
					"userName":         user,
					"clusterName":      managementCluster.GetName(),
					"roleTemplateName": "cluster-owner"})
			if err != nil {
				return fmt.Errorf("failed to create cluster role template binding to add user [%s] as a cluster owner to the vCluster's rancher cluster [%s]: %w", user, managementCluster.GetName(), err)
			}
			return nil
		})
	if len(forEachErrors) > 0 {
		return fmt.Errorf("failed to to create %d cluster role template bindings for vCluster's rancher cluster: %w", len(forEachErrors), gerrors.AggregateError(forEachErrors))
	}

	// cleanup ClusterRoleTemplateBindings
	requirement, err = labels.NewRequirement(constants.LabelVClusterServiceUID, selection.Equals, []string{managementCluster.GetLabels()[constants.LabelVClusterServiceUID]})
	if err != nil {
		return fmt.Errorf("failed to create requirement that filters for ClusterRoleTemplateBindings with loft.sh/vcluster-service-uid label: %w", err)
	}

	crtbs, err := r.Client.ListWithOptions(ctx, gvk.ClusterRoleTemplateBindingManagementCattle, &client.ListOptions{Namespace: managementCluster.GetName(), LabelSelector: labels.NewSelector().Add(*requirement)})
	if err != nil {
		return fmt.Errorf("failed to list ClusterRoleTemplateBindings that target vCluster's rancher cluster [%s]: %w", managementCluster.GetName(), err)
	}

	if len(crtbs.Items) == 0 {
		return nil
	}

	forEachErrors = unstructured.ForEachItem(crtbs, func(index int, item v1unstructured.Unstructured) error {
		user := unstructured.GetNested[string](item.Object, "userName")
		if user == "" {
			return fmt.Errorf("user cannot be empty string")
		}

		if _, ok := clusterOwners[user]; ok {
			// CRTB is still backed by valid RTB, no-op
			return nil
		}
		err = r.Client.Delete(ctx, &item)
		if err != nil && !kerrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete crtb mapped to deleted PRTB [%s/%s]: %w", item.GetNamespace(), item.GetName(), err)
		}
		return nil
	})
	if forEachErrors != nil {
		return gerrors.AggregateError(forEachErrors)
	}
	return nil
}

func (r *ClusterReconciler) SyncCleanup(ctx context.Context, logger logr.Logger, managementCluster v1unstructured.Unstructured) error {
	if managementCluster.GetDeletionTimestamp() == nil {
		return nil
	}

	if managementCluster.GetLabels()[constants.LabelTargetApp] == "" {
		return nil
	}

	logger = logger.WithValues("vclusterAppName", managementCluster.GetLabels()[constants.LabelTargetApp])
	appNamespace, appName, err := parseHalves(managementCluster.GetLabels()[constants.LabelTargetApp], "_")
	if err != nil {
		return fmt.Errorf("failed to parse app namespace and name from management clusters %q annotation", constants.LabelTargetApp)
	}

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/v1/catalog.cattle.io.apps/%s/%s?", rancher.GetClusterEndpoint(managementCluster.GetLabels()[constants.LabelHostClusterName]), appNamespace, appName)+url.PathEscape("action=uninstall"), bytes.NewBuffer([]byte("{}")))
	if err != nil {
		return fmt.Errorf("could not create request for vcluster app uninstal: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.RancherToken)
	req.Method = "POST"
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fai")
	}

	obj := &struct {
		Code string `json:"code,omitempty"`
	}{}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("could not read body for response to rancher app uninstall: %w", err)
	}

	err = json.Unmarshal(body, obj)
	if err != nil {
		return fmt.Errorf("could not unmarshal response from rancher app uninstall: %w", err)
	}

	if resp.StatusCode != http.StatusOK && obj.Code != "NotFound" {
		return errors.New("app uninstall failed")
	}

	logger.Info("cleaning up...")
	if !slices.Contains(managementCluster.GetFinalizers(), constants.FinalizerVClusterApp) {
		logger.Info("nothing to cleanup, exiting")
		return nil
	}

	for index, value := range managementCluster.GetFinalizers() {
		if value == constants.FinalizerVClusterApp {
			orig := managementCluster.DeepCopy()
			managementCluster.SetFinalizers(slices.Delete(managementCluster.GetFinalizers(), index, index+1))
			err = r.Client.Patch(ctx, &managementCluster, client.MergeFrom(orig))
			if err != nil && !kerrors.IsNotFound(err) {
				return fmt.Errorf("failed to remov")
			}
			break
		}
	}
	return nil
}

// SyncResourceLimits uses information from the vCluster's resource quota (if configured), to display the vCluster
// limits rather than the host cluster's resources.
func (r *ClusterReconciler) SyncResourceLimits(ctx context.Context, logger logr.Logger, managementCluster v1unstructured.Unstructured) error {
	hostClusterName := managementCluster.GetLabels()[constants.LabelHostClusterName]
	vClusterServiceUID := managementCluster.GetLabels()[constants.LabelVClusterServiceUID]

	// not a vcluster management cluster or missing required labels
	if hostClusterName == "" || vClusterServiceUID == "" {
		return nil
	}

	// Get the host cluster client to find the vCluster service
	restConfig, err := token.RestConfigFromToken(hostClusterName, r.RancherToken)
	if err != nil {
		return fmt.Errorf("failed to get rest config for host cluster: %w", err)
	}

	hostClusterClient, _, err := getClusterClient(restConfig)
	if err != nil {
		return fmt.Errorf("failed to get host cluster client: %w", err)
	}

	// Find the vCluster service by UID
	svcs, err := hostClusterClient.CoreV1().Services("").List(ctx, metav1.ListOptions{
		LabelSelector: "app=vcluster",
	})
	if err != nil {
		return fmt.Errorf("failed to list vCluster services: %w", err)
	}

	var vClusterService *corev1.Service
	for i := range svcs.Items {
		if string(svcs.Items[i].GetUID()) == vClusterServiceUID {
			vClusterService = &svcs.Items[i]
			break
		}
	}

	if vClusterService == nil {
		logger.V(1).Info("vCluster service not found, skipping resource metrics sync")
		return nil
	}

	resourceQuota, err := hostClusterClient.CoreV1().ResourceQuotas(vClusterService.Namespace).Get(ctx, "vc-"+vClusterService.Name, metav1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("failed to get resource quota: %w", err)
	}

	hard := resourceQuota.Status.Hard

	orig := managementCluster.DeepCopy()
	status := unstructured.GetNested[map[string]any](managementCluster.Object, "status")
	if status == nil {
		status = map[string]any{}
	}

	capacity, _ := status["capacity"].(map[string]any)
	allocatable, _ := status["allocatable"].(map[string]any)
	requested, _ := status["requested"].(map[string]any)

	for _, resource := range []string{"cpu", "memory"} {
		limit, hasLimit := hard[corev1.ResourceName("limits."+resource)]

		if hasLimit {
			capacity[resource] = limit.String()
			allocatable[resource] = limit.String()
		}

		req, hasRequest := hard[corev1.ResourceName("requests."+resource)]
		if hasRequest {
			requested[resource] = req.String()
		}
	}

	if podCount, hasPodCount := hard[corev1.ResourceName("count/pods")]; hasPodCount {
		allocatable["pods"] = podCount
	}

	status["capacity"] = capacity
	status["allocatable"] = allocatable
	status["requested"] = requested
	managementCluster.Object["status"] = status

	if err := r.Client.Patch(ctx, &managementCluster, client.MergeFrom(orig)); err != nil {
		return fmt.Errorf("failed to update management cluster status with resource metrics: %w", err)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Watches(gvk.ToUnstructured(gvk.ProjectRoleTemplateBindingManagementCattle),
			handler.EnqueueRequestsFromMapFunc(r.clustersRelatedToTargetProject),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(gvk.ToUnstructured(gvk.ClusterRoleTemplateBindingManagementCattle),
			handler.EnqueueRequestsFromMapFunc(r.clustersHostedByClusterTarget),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		For(gvk.ToUnstructured(gvk.ClustersManagementCattle)).
		Named("cluster").
		Complete(r)
}

func (r *ClusterReconciler) clustersRelatedToTargetProject(ctx context.Context, obj client.Object) []reconcile.Request {
	prtb := obj.(*v1unstructured.Unstructured)
	clusterID, projectID, err := parseHalves(unstructured.GetNested[string](prtb.Object, "projectName"), ":")
	if err != nil {
		return nil
	}

	project, err := r.Client.Get(ctx, gvk.ProjectManagementCattle, projectID, clusterID)
	if err != nil {
		return nil
	}

	clusters, err := r.Client.ListWithLabel(ctx, gvk.ClustersManagementCattle, constants.LabelProjectUID, string(project.GetUID()))
	if err != nil {
		return nil
	}

	requests := make([]reconcile.Request, len(clusters.Items))
	unstructured.ForEachItem(clusters, func(index int, item v1unstructured.Unstructured) error {
		requests[index] = reconcile.Request{NamespacedName: types.NamespacedName{Name: item.GetName()}}
		return nil
	})
	return requests
}

func (r *ClusterReconciler) clustersHostedByClusterTarget(ctx context.Context, obj client.Object) []reconcile.Request {
	crtb := obj.(*v1unstructured.Unstructured)
	clusterName := unstructured.GetNested[string](crtb.Object, "clusterName")

	clusters, err := r.Client.ListWithLabel(ctx, gvk.ClustersManagementCattle, constants.LabelHostClusterName, clusterName)
	if err != nil {
		return nil
	}

	requests := make([]reconcile.Request, len(clusters.Items))
	unstructured.ForEachItem(clusters, func(index int, item v1unstructured.Unstructured) error {
		requests[index] = reconcile.Request{NamespacedName: types.NamespacedName{Name: item.GetName()}}
		return nil
	})
	return requests
}

// SyncStaleResources checks if the vCluster service still exists in the host cluster.
// If the service is gone, this deletes the management cluster (and its provisioning cluster via owner reference).
// This handles the case where services were deleted while the operator was offline.
func (r *ClusterReconciler) SyncStaleResources(ctx context.Context, logger logr.Logger, managementCluster v1unstructured.Unstructured) error {
	serviceUID := managementCluster.GetLabels()[constants.LabelVClusterServiceUID]
	hostClusterName := managementCluster.GetLabels()[constants.LabelHostClusterName]

	// not a vCluster management cluster
	if serviceUID == "" || hostClusterName == "" {
		return nil
	}

	// get the host cluster client
	r.lock.RLock()
	_, hasInformer := r.peers[hostClusterName]
	r.lock.RUnlock()

	if !hasInformer {
		// informer not yet started for this cluster, skip check
		return nil
	}

	restConfig, err := token.RestConfigFromToken(hostClusterName, r.RancherToken)
	if err != nil {
		return fmt.Errorf("failed to get rest config for host cluster %s: %w", hostClusterName, err)
	}

	clusterClient, _, err := getClusterClient(restConfig)
	if err != nil {
		return fmt.Errorf("failed to get cluster client for host cluster %s: %w", hostClusterName, err)
	}

	// list all services with app=vcluster label
	services, err := clusterClient.CoreV1().Services("").List(ctx, metav1.ListOptions{
		LabelSelector: "app=vcluster",
	})
	if err != nil {
		return fmt.Errorf("failed to list services in host cluster %s: %w", hostClusterName, err)
	}

	// check if service with matching UID exists
	for _, svc := range services.Items {
		if string(svc.UID) == serviceUID {
			// service exists, not stale
			return nil
		}
	}

	// service not found - this is stale, delete the management cluster
	logger.Info("vCluster service no longer exists, deleting stale management cluster",
		"managementCluster", managementCluster.GetName(),
		"serviceUID", serviceUID,
		"hostCluster", hostClusterName)

	if err := r.Client.Delete(ctx, &managementCluster); err != nil && !kerrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete stale management cluster %s: %w", managementCluster.GetName(), err)
	}

	return nil
}

func parseHalves(name, separator string) (string, string, error) {
	parts := strings.Split(name, separator)
	if len(parts) != 2 {
		return "", "", errors.New("invalid project name [%s], expect to have the format <cluster-id:project-id>")
	}
	return parts[0], parts[1], nil
}
