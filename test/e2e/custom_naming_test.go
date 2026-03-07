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
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/loft-sh/vcluster-rancher-operator/pkg/constants"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/unstructured/gvk"
	"github.com/loft-sh/vcluster-rancher-operator/test/utils"
)

//nolint:dupl
var _ = Describe("VCluster Custom Name Annotation", Ordered, func() {
	const (
		vclusterName = "test-vc-custom-name"
		vclusterNS   = "e2e-test-custom-name"
		customName   = "my-custom-rancher-cluster"
	)

	var (
		serviceUID string
		valuesFile string
	)

	BeforeAll(func() {
		By("creating test namespace")
		createNamespace(vclusterNS)

		By("writing vcluster values file with custom name annotation")
		f, err := os.CreateTemp("", "vcluster-values-*.yaml")
		Expect(err).NotTo(HaveOccurred())
		valuesFile = f.Name()
		_, err = f.WriteString(fmt.Sprintf(`controlPlane:
  service:
    annotations:
      platform.vcluster.com/custom-name: %s
`, customName))
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Close()).To(Succeed())
	})

	AfterAll(func() {
		if valuesFile != "" {
			_ = os.Remove(valuesFile)
		}

		By("deleting vcluster")
		cmd := exec.Command("vcluster", "delete", vclusterName, "--namespace", vclusterNS)
		_, _ = utils.Run(cmd)

		By("deleting test namespace")
		_ = hostClient.CoreV1().Namespaces().Delete(
			context.Background(), vclusterNS, metav1.DeleteOptions{},
		)
	})

	It("should create a Rancher provisioning cluster with the custom name", func() {
		By("creating a vcluster with the custom name annotation on its service")
		cmd := exec.Command("vcluster", "create", vclusterName,
			"--driver", "helm",
			"--namespace", vclusterNS,
			"--connect=false",
			"--values", valuesFile,
		)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for the vcluster service to appear")
		Eventually(func(g Gomega) {
			svc, err := hostClient.CoreV1().Services(vclusterNS).Get(
				context.Background(), vclusterName, metav1.GetOptions{},
			)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(string(svc.UID)).NotTo(BeEmpty())
			serviceUID = string(svc.UID)
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for the operator to create a provisioning cluster with the custom name")
		Eventually(func(g Gomega) {
			cluster, err := rancherClient.GetFirstWithLabel(context.Background(), gvk.ClusterProvisioningCattle, constants.LabelVClusterServiceUID, serviceUID)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(cluster.GetName()).To(Equal(customName))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})
})

//nolint:dupl
var _ = Describe("VCluster Custom Name Prefix Annotation", Ordered, func() {
	const (
		vclusterName = "test-vc-custom-prefix"
		vclusterNS   = "e2e-test-custom-prefix"
		customPrefix = "my-prefix-"
	)

	var (
		serviceUID string
		valuesFile string
	)

	BeforeAll(func() {
		By("creating test namespace")
		createNamespace(vclusterNS)

		By("writing vcluster values file with custom name prefix annotation")
		f, err := os.CreateTemp("", "vcluster-values-*.yaml")
		Expect(err).NotTo(HaveOccurred())
		valuesFile = f.Name()
		_, err = f.WriteString(fmt.Sprintf(`controlPlane:
  service:
    annotations:
      platform.vcluster.com/custom-name-prefix: %s
`, customPrefix))
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Close()).To(Succeed())
	})

	AfterAll(func() {
		if valuesFile != "" {
			_ = os.Remove(valuesFile)
		}

		By("deleting vcluster")
		cmd := exec.Command("vcluster", "delete", vclusterName, "--namespace", vclusterNS)
		_, _ = utils.Run(cmd)

		By("deleting test namespace")
		_ = hostClient.CoreV1().Namespaces().Delete(
			context.Background(), vclusterNS, metav1.DeleteOptions{},
		)
	})

	It("should create a Rancher provisioning cluster with a name starting with the custom prefix", func() {
		By("creating a vcluster with the custom name prefix annotation on its service")
		cmd := exec.Command("vcluster", "create", vclusterName,
			"--driver", "helm",
			"--namespace", vclusterNS,
			"--connect=false",
			"--values", valuesFile,
		)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("waiting for the vcluster service to appear")
		Eventually(func(g Gomega) {
			svc, err := hostClient.CoreV1().Services(vclusterNS).Get(
				context.Background(), vclusterName, metav1.GetOptions{},
			)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(string(svc.UID)).NotTo(BeEmpty())
			serviceUID = string(svc.UID)
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for the operator to create a provisioning cluster with a name starting with the custom prefix")
		Eventually(func(g Gomega) {
			cluster, err := rancherClient.GetFirstWithLabel(context.Background(), gvk.ClusterProvisioningCattle, constants.LabelVClusterServiceUID, serviceUID)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(cluster.GetName()).To(HavePrefix(customPrefix))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})
})
