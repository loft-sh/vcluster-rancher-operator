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
	"fmt"
	"os"
	"sync"

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
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	clusterEndpointFmt = "https://rancher.cattle-system/k8s/clusters/%s"
)

var (
	rancherToken = ""
)

func init() {
	rancherToken = os.Getenv(rancherToken)
}

// ClusterReconciler reconciles a Cluster object
type ClusterReconciler struct {
	Client unstructured.Client
	Scheme *runtime.Scheme

	lock         sync.RWMutex
	rancherToken string
	peers        map[string]struct{}
}

type data struct {
	ClusterName string
	Host        string
	ClusterID   string
	Token       string
}

// +kubebuilder:rbac:groups=management.cattle.io,resources=clusters;tokens,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=management.cattle.io,resources=clusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=management.cattle.io,resources=clusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=management.cattle.io,resources=users,verbs=list
// +kubebuilder:rbac:groups=provisioning.cattle.io,resources=clusters;tokens,verbs=get;list;create;update;delete

// Reconcile
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var ok bool
	func() {
		r.lock.Lock()
		defer r.lock.Unlock()
		_, ok = r.peers[req.Name]
	}()

	if ok {
		return ctrl.Result{}, nil
	}

	var tokenNameAndValue string
	func() {
		r.lock.RLock()
		defer r.lock.RUnlock()
		tokenNameAndValue = r.rancherToken
	}()

	if tokenNameAndValue == "" {
		tokenList, err := r.Client.ListWithLabel(ctx, schema.GroupVersionKind{Kind: "token", Group: "management.cattle.io", Version: "v3"}, "loft.sh/vcluster-rancher-system-token", "true")
		if err != nil {
			return ctrl.Result{}, err
		}

		for index := range tokenList.Items {
			err = r.Client.Delete(ctx, &tokenList.Items[index])
			if err != nil {
				return ctrl.Result{}, err
			}
		}

		systemUser, err := r.Client.GetFirstWithLabel(ctx, schema.GroupVersionKind{Kind: "User", Group: "management.cattle.io", Version: "v3"}, "loft.sh/vcluster-rancher-user", "true")
		if err != nil {
			return ctrl.Result{}, err
		}

		tokenValue, err := randomtoken.Generate()
		if err != nil {
			return ctrl.Result{}, err
		}

		_, err = r.Client.Create(
			ctx,
			schema.GroupVersionKind{Kind: "Token", Group: "management.cattle.io", Version: "v3"},
			"token-vcluster-rancher-op",
			"",
			false,
			map[string]string{"loft.sh/vcluster-rancher-system-token": "true"},
			map[string]interface{}{
				"authProvider": "local",
				"userId":       systemUser.GetName(),
				"token":        tokenValue,
			})
		err = func() error {
			tokenNameAndValue = fmt.Sprintf("token-vcluster-rancher-op:%s", tokenValue)
			r.lock.Lock()
			defer r.lock.Unlock()
			r.rancherToken = tokenNameAndValue
			restConfig, err := restConfigFromToken("local", tokenNameAndValue)
			if err != nil {
				return err
			}
			unstructuredClusterClient, err := cluster.New(restConfig)
			if err != nil {
				return err
			}
			r.Client = unstructured.Client{Client: unstructuredClusterClient.GetClient()}
			return nil
		}()
	}

	restConfig, err := restConfigFromToken(req.Name, tokenNameAndValue)
	if err != nil {
		return ctrl.Result{}, err
	}

	clusterClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return ctrl.Result{}, err
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
		return ctrl.Result{}, err
	}
	_, err = sharedInformer.AddEventHandler(&services.Handler{
		Ctx:                     ctx,
		Logger:                  logger,
		LocalUnstructuredClient: r.Client,
		ClusterUnstructuredClient: unstructured.Client{
			Client: unstructuredClusterClient.GetClient(),
		},
		ClusterClient: clusterClient,
		ClusterName:   req.Name,
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	func() {
		r.lock.Lock()
		defer r.lock.Unlock()
		if r.peers == nil {
			r.peers = make(map[string]struct{})
		}
		r.peers[req.Name] = struct{}{}
	}()

	go func() {
		logger.Info(fmt.Sprintf("starting handler for %s's vclusters\n", req.Name))
		sharedInformer.Run(ctx.Done())
		logger.Info(fmt.Sprintf("finished running handler for %s's vclusters\n", req.Name))
	}()
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

func ForTokenBased(clusterID, host, token string) (string, error) {
	// this code is mostly taken from rancher/wrangler
	data := &data{
		ClusterName: "",
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

func restConfigFromToken(clusterID, token string) (*rest.Config, error) {
	kubeConfig, err := ForTokenBased(clusterID, fmt.Sprintf(clusterEndpointFmt, clusterID), token)
	if err != nil {
		return nil, err
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeConfig))
	if err != nil {
		return nil, err
	}
	restConfig.Insecure = true

	return restConfig, nil
}
