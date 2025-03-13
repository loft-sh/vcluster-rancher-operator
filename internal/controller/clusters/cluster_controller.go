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
	"context"
	errors3 "errors"
	"fmt"
	"github.com/go-logr/logr"
	"strings"
	"sync"

	"github.com/loft-sh/vcluster-rancher-op/pkg/services"
	"github.com/loft-sh/vcluster-rancher-op/pkg/token"
	"github.com/loft-sh/vcluster-rancher-op/pkg/unstructured"
	errors2 "github.com/onsi/gomega/gstruct/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1unstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
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

// ClusterReconciler reconciles a Cluster object
type ClusterReconciler struct {
	Client unstructured.Client
	Scheme *runtime.Scheme

	sync.Map
	lock         sync.RWMutex
	RancherToken string
	peers        map[string]struct{}
}

// +kubebuilder:rbac:groups=management.cattle.io,resources=clusters;tokens,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=management.cattle.io,resources=clusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=management.cattle.io,resources=clusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=management.cattle.io,resources=users,verbs=list
// +kubebuilder:rbac:groups=provisioning.cattle.io,resources=clusters;tokens,verbs=get;list;create;update;delete

// Reconcile
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.Info("enqueue " + req.Name)
	managementCluster, err := r.Client.Get(ctx, schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "Cluster"}, req.Name, "")
	if err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("debugenq 1")
	err = r.SyncRancherRBAC(ctx, logger, managementCluster)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = r.SyncvClusterInstallHandler(ctx, logger, managementCluster.GetName())
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

type TestCluster struct {
	metav1.ObjectMeta
	metav1.TypeMeta
}

func (r *ClusterReconciler) SyncvClusterInstallHandler(ctx context.Context, logger logr.Logger, clusterName string) error {
	r.lock.Lock()
	defer r.lock.Unlock()
	_, ok := r.peers[clusterName]

	if ok {
		return nil
	}

	restConfig, err := token.RestConfigFromToken(clusterName, r.RancherToken)
	if err != nil {
		return err
	}

	clusterClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	sharedInformer := cache.NewSharedInformer(&cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			options.LabelSelector = "app=vcluster"
			return clusterClient.CoreV1().Services("").List(ctx, options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			options.LabelSelector = "app=vcluster"
			return clusterClient.CoreV1().Services("").Watch(ctx, options)
		},
	}, &v1.Service{}, 0)

	unstructuredClusterClient, err := cluster.New(restConfig)
	if err != nil {
		return err
	}
	_, err = sharedInformer.AddEventHandler(&services.Handler{
		Ctx:                     ctx,
		Logger:                  logger,
		LocalUnstructuredClient: r.Client,
		ClusterUnstructuredClient: unstructured.Client{
			Client: unstructuredClusterClient.GetClient(),
		},
		ClusterClient: clusterClient,
		ClusterName:   clusterName,
	})
	if err != nil {
		return err
	}

	if r.peers == nil {
		r.peers = make(map[string]struct{})
	}
	r.peers[clusterName] = struct{}{}

	go func() {
		logger.Info(fmt.Sprintf("starting handler for %s's vclusters\n", clusterName))
		sharedInformer.Run(ctx.Done())
		logger.Info(fmt.Sprintf("finished running handler for %s's vclusters\n", clusterName))
	}()
	return nil
}

// TODO move this to reocncile, delete rbac logic in services handler, and test
func (r *ClusterReconciler) SyncRancherRBAC(ctx context.Context, logger logr.Logger, managementCluster v1unstructured.Unstructured) error {
	projectUID := managementCluster.GetLabels()["loft.sh/vcluster-project-uid"]
	projectName := managementCluster.GetLabels()["loft.sh/vcluster-project"]
	hostClusterName := managementCluster.GetLabels()["loft.sh/vcluster-host-cluster"]

	if projectUID == "" && projectName == "" && hostClusterName == "" {
		// not a vcluster management cluster
		return nil
	}

	if projectName == "" || projectUID == "" || hostClusterName == "" {
		return errors3.New("vCluster management cluster missing at least 1 vCluster label(s)")
	}

	project, err := r.Client.Get(ctx, schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "Project"}, projectName, hostClusterName)
	if err != nil {
		return fmt.Errorf("failed to get vCluster's project [%s/%s]: %w", managementCluster.GetName(), projectName, err)
	}

	if string(project.GetUID()) != projectUID {
		return fmt.Errorf("vCluster was installed in project [%[1]s] with UID [%[2]s]. Current project [%[1]s] has mismatched UID [%[3]s]", projectName, projectUID, project.GetUID())
	}

	projectRoleTemplateBindings, err := r.Client.List(ctx, schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "ProjectRoleTemplateBinding"}, project.GetName())
	if err != nil {
		return fmt.Errorf("failed to list projectRoleTemplateBindings that target vCluster's project [%s]: %w", project.GetName(), err)
	}

	projectRoleTemplateBindings = unstructured.FilterItems[string](projectRoleTemplateBindings, fmt.Sprintf("%s:%s", project.GetNamespace(), project.GetName()), true, "projectName")
	projectRoleTemplateBindings.Items = append(unstructured.FilterItems[string](projectRoleTemplateBindings, "project-owner", true, "roleTemplateName").Items, unstructured.FilterItems[string](projectRoleTemplateBindings, "project-member", true, "roleTemplateName").Items...)

	forEachErrors := unstructured.ForEachItem(
		projectRoleTemplateBindings,
		func(_ int, item v1unstructured.Unstructured) error {
			user := unstructured.GetNested[string](item.Object, "userName")
			if user == "" {
				return fmt.Errorf("user cannot be empty string")
			}

			_, err := r.Client.Get(ctx, schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "ClusterRoleTemplateBinding"}, fmt.Sprintf("vc-%s-co", item.GetName()), managementCluster.GetName())
			if err != nil && !errors.IsNotFound(err) {
				return err
			}

			if err == nil {
				return nil
			}

			_, err = r.Client.Create(
				ctx,
				schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "ClusterRoleTemplateBinding"},
				fmt.Sprintf("vc-%s-co", item.GetName()),
				managementCluster.GetName(), false,
				map[string]string{
					"loft.sh/vcluster-service-uid": managementCluster.GetLabels()["loft.sh/vcluster-service-uid"],
					"loft.sh/parent-prtb":          item.GetName(),
				},
				map[string]interface{}{
					"userName":         user,
					"clusterName":      managementCluster.GetName(),
					"roleTemplateName": "cluster-owner"})
			if err != nil {
				return fmt.Errorf("failed to create cluster role template binding to add user [%s] as a cluster owner to the vCluster's rancher cluster [%s]: %w", user, managementCluster.GetName(), err)
			}
			return nil
		})
	if len(forEachErrors) > 0 {
		return fmt.Errorf("failed to to create %d cluster role template bindings for vCluster's rancher cluster: %w", len(forEachErrors), errors2.AggregateError(forEachErrors))
	}

	// cleanup ClusterRoleTemplateBindings
	crtbs, err := r.Client.List(ctx, schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "ClusterRoleTemplateBinding"}, managementCluster.GetName())
	if err != nil {
		return fmt.Errorf("failed to list ClusterRoleTemplateBindings that target vCluster's Rancher clluster [%s]: %w", managementCluster.GetName(), err)
	}

	if len(crtbs.Items) == 0 {
		return nil
	}

	forEachErrors = unstructured.ForEachItem(crtbs, func(index int, item v1unstructured.Unstructured) error {
		parentPRTBName := item.GetLabels()["loft.sh/parent-prtb"]
		if parentPRTBName == "" {
			return nil
		}

		parentPRTB, err := r.Client.Get(ctx, schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "ProjectRoleTemplateBinding"}, parentPRTBName, projectName)
		if err != nil && !errors.IsNotFound(err) {
			// returns on nil as well
			return fmt.Errorf("failed to get parent PRTB [%s] for CRTB [%s]: %w", parentPRTBName, fmt.Sprintf("%s/%s", item.GetNamespace(), item.GetName()), err)
		}

		parentPRTBRole := unstructured.GetNested[string](parentPRTB.Object, "roleTemplateName")
		if parentPRTBRole == "project-owner" || parentPRTBRole == "project-member" {
			// CRTB is still backed by valid PRTB, no-op
			return nil
		}

		err = r.Client.Delete(ctx, &item)
		if err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete crtb mapped to deleted PRTB [%s/%s]: %w", item.GetNamespace(), item.GetName(), err)
		}
		return nil
	})
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Watches(&v1unstructured.Unstructured{Object: map[string]interface{}{"kind": "ProjectRoleTemplateBinding", "apiVersion": "management.cattle.io/v3"}}, handler.EnqueueRequestsFromMapFunc(r.clustersRelatedToTargetProject), builder.WithPredicates(predicate.ResourceVersionChangedPredicate{})).
		// Uncomment the following line adding a pointer to an instance of the controlled resource as an argument
		For(&v1unstructured.Unstructured{Object: map[string]interface{}{"kind": "Cluster", "apiVersion": "management.cattle.io/v3"}}).
		Named("cluster").
		Complete(r)
}

func (r *ClusterReconciler) clustersRelatedToTargetProject(ctx context.Context, obj client.Object) []reconcile.Request {
	prtb := obj.(*v1unstructured.Unstructured)
	clusterID, projectID, err := parseHalves(unstructured.GetNested[string](prtb.Object, "projectName"), ":")
	if err != nil {
		return nil
	}

	project, err := r.Client.Get(ctx, schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "Project"}, projectID, clusterID)
	if err != nil {
		return nil
	}

	clusters, err := r.Client.ListWithLabel(ctx, schema.GroupVersionKind{Group: "management.cattle.io", Version: "v3", Kind: "Cluster"}, "loft.sh/vcluster-project-uid", string(project.GetUID()))
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

func parseHalves(name, separator string) (string, string, error) {
	parts := strings.Split(name, separator)
	if len(parts) != 2 {
		return "", "", errors3.New("invalid project name [%s], expect to have the format <cluster-id:project-id>")
	}
	return parts[0], parts[1], nil
}
