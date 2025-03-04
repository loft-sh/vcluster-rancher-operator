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

package controller

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sync"

	"github.com/loft-sh/vcluster-rancher-op/pkg/clustermanager"
	"github.com/loft-sh/vcluster-rancher-op/pkg/services"
	"github.com/loft-sh/vcluster-rancher-op/pkg/unstructured"
	"github.com/rancher/wrangler/pkg/randomtoken"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1unstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	IntegrationRancherUser = "VCLUSTER_RANCHER_USER"
	clusterEndpointFmt     = "https://rancher.cattle-system/k8s/clusters/%s"
)

var (
	rancherToken = ""
)

func init() {
	rancherToken = os.Getenv(rancherToken)
}

// ClusterReconciler reconciles a Cluster object
type ClusterReconciler struct {
	Client         unstructured.Client
	Scheme         *runtime.Scheme
	ClusterManager *clustermanager.Manager

	tokenLock    sync.RWMutex
	rancherToken string
	peers        map[string]string
}

type data struct {
	ClusterName string
	Host        string
	ClusterID   string
	Token       string
}

// +kubebuilder:rbac:groups=management.cattle.io,resources=clusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=management.cattle.io,resources=clusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=management.cattle.io,resources=clusters/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Cluster object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	fmt.Println("print works")
	logger.Info("logger works")
	_, ok := r.peers[req.Name]
	if ok {
		return ctrl.Result{}, nil
	}

	if req.Name == "c-m-6z5mhg2b" || req.Name == "c-m-fmsf4k4w" {
		return ctrl.Result{}, nil
	}
	r.tokenLock.RLock()
	tokenNameAndValue := r.rancherToken
	r.tokenLock.RUnlock()
	if tokenNameAndValue == "" {
		tokenList, err := r.Client.ListWithLabels(ctx, schema.GroupVersionKind{Kind: "token", Group: "management.cattle.io", Version: "v3"}, "loft.sh/vcluster-rancher-system-token", "true")
		if err != nil {
			return ctrl.Result{}, err
		}

		for index := range tokenList.Items {
			err = r.Client.Delete(ctx, &tokenList.Items[index])
			if err != nil {
				return ctrl.Result{}, err
			}
		}

		systemUser := os.Getenv(IntegrationRancherUser)
		tokenValue, err := randomtoken.Generate()
		if err != nil {
			return ctrl.Result{}, err
		}

		_, err = r.Client.Create(
			ctx,
			schema.GroupVersionKind{Kind: "Token", Group: "management.cattle.io", Version: "v3"},
			"token-vcluster-rancher-op",
			"",
			map[string]string{"loft.sh/vcluster-rancher-system-token": "true"},
			map[string]interface{}{
				"authProvider": "local",
				"userId":       systemUser,
				"token":        tokenValue,
			})

		tokenNameAndValue = fmt.Sprintf("token-vcluster-rancher-op:%s", tokenValue)
		r.tokenLock.Lock()
		r.rancherToken = tokenNameAndValue
		r.tokenLock.Unlock()
	}

	kubeConfig, err := ForTokenBased("", req.Name, fmt.Sprintf(clusterEndpointFmt, req.Name), tokenNameAndValue)
	if err != nil {
		return ctrl.Result{}, err
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeConfig))
	if err != nil {
		return ctrl.Result{}, err
	}
	restConfig.Insecure = true

	clusterClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return ctrl.Result{}, err
	}

	sharedInformer := cache.NewSharedInformer(&cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			options.LabelSelector = "app=vcluster"
			fmt.Println("list services for", req.Name)
			return clusterClient.CoreV1().Services("").List(ctx, options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			options.LabelSelector = "app=vcluster"
			return clusterClient.CoreV1().Services("").Watch(ctx, options)
		},
	}, &v1.Service{}, 0)

	downstreamCluster, err := cluster.New(restConfig)
	if err != nil {
		return ctrl.Result{}, err
	}

	_, err = sharedInformer.AddEventHandler(&services.Handler{
		Ctx:         ctx,
		LocalClient: r.Client,
		ClusterClient: unstructured.Client{
			Client: downstreamCluster.GetClient(),
		},
		ClusterName: req.Name,
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	sharedInformer.Run(ctx.Done())
	sharedInformer.HasSynced()

	fmt.Println("synced", req.Name)
	return ctrl.Result{}, nil
}

type TestCluster struct {
	metav1.ObjectMeta
	metav1.TypeMeta
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Uncomment the following line adding a pointer to an instance of the controlled resource as an argument
		For(&v1unstructured.Unstructured{Object: map[string]interface{}{"kind": "Cluster", "apiVersion": "management.cattle.io/v3"}}).
		Named("cluster").
		Complete(r)
}

func ForTokenBased(clusterName, clusterID, host, token string) (string, error) {
	data := &data{
		ClusterName: clusterName,
		ClusterID:   clusterID,
		Host:        host,
		Token:       token,
	}

	if data.ClusterName == "" {
		data.ClusterName = data.ClusterID
	}

	buf := &bytes.Buffer{}
	err := tokenTemplate.Execute(buf, data)
	return buf.String(), err
}
