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

package e2e

import (
	"fmt"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	pkgunstructured "github.com/loft-sh/vcluster-rancher-operator/pkg/unstructured"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	hostClient    kubernetes.Interface
	rancherClient *pkgunstructured.Client
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting vcluster-rancher-operator integration test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	rancherHost := os.Getenv("RANCHER_HOST")
	rancherToken := os.Getenv("RANCHER_TOKEN")
	Expect(rancherHost).NotTo(BeEmpty(), "RANCHER_HOST env var must be set")
	Expect(rancherToken).NotTo(BeEmpty(), "RANCHER_TOKEN env var must be set")

	cfg, err := ctrl.GetConfig()
	Expect(err).NotTo(HaveOccurred())
	hostClient, err = kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())

	rancherCfg := &rest.Config{
		Host:        rancherHost + "/k8s/clusters/local",
		BearerToken: rancherToken,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
		},
	}
	rancherCtrlClient, err := client.New(rancherCfg, client.Options{})
	Expect(err).NotTo(HaveOccurred())
	rancherClient = &pkgunstructured.Client{Client: rancherCtrlClient}
})
