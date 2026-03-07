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

var _ = Describe("VCluster Resource Quota Sync", Ordered, func() {
	const (
		vclusterName  = "test-vc-quota"
		vclusterNS    = "e2e-test-quota"
		cpuLimit      = "8"
		memoryLimit   = "16Gi"
		cpuRequest    = "4"
		memoryRequest = "8Gi"
		podCount      = "10"
	)

	var (
		serviceUID string
		valuesFile string
	)

	BeforeAll(func() {
		By("creating test namespace")
		createNamespace(vclusterNS)

		By("writing vcluster values file with resource quota")
		f, err := os.CreateTemp("", "vcluster-values-*.yaml")
		Expect(err).NotTo(HaveOccurred())
		valuesFile = f.Name()
		_, err = f.WriteString(fmt.Sprintf(`policies:
  resourceQuota:
    enabled: true
    quota:
      limits.cpu: "%s"
      limits.memory: "%s"
      requests.cpu: "%s"
      requests.memory: "%s"
      count/pods: "%s"
`, cpuLimit, memoryLimit, cpuRequest, memoryRequest, podCount))
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

	It("should sync resource quota limits to the Rancher management cluster status", func() {
		By("creating a vcluster with resource quota configured")
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

		By("waiting for the Rancher management cluster to become Active (Ready=True)")
		Eventually(func(g Gomega) {
			cluster, err := rancherClient.GetFirstWithLabel(context.Background(), gvk.ClustersManagementCattle, constants.LabelVClusterServiceUID, serviceUID)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(isReady(cluster)).To(BeTrue(),
				"management cluster conditions: %v", conditionSummary(cluster))
		}, 10*time.Minute, 15*time.Second).Should(Succeed())

		By("waiting for the operator to sync resource quota limits to the management cluster status")
		Eventually(func(g Gomega) {
			cluster, err := rancherClient.GetFirstWithLabel(context.Background(), gvk.ClustersManagementCattle, constants.LabelVClusterServiceUID, serviceUID)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(nestedString(cluster, "status", "capacity", "cpu")).To(Equal(cpuLimit), "capacity.cpu")
			g.Expect(nestedString(cluster, "status", "capacity", "memory")).To(Equal(memoryLimit), "capacity.memory")
			g.Expect(nestedString(cluster, "status", "allocatable", "cpu")).To(Equal(cpuLimit), "allocatable.cpu")
			g.Expect(nestedString(cluster, "status", "allocatable", "memory")).To(Equal(memoryLimit), "allocatable.memory")
			g.Expect(nestedString(cluster, "status", "requested", "cpu")).To(Equal(cpuRequest), "requested.cpu")
			g.Expect(nestedString(cluster, "status", "requested", "memory")).To(Equal(memoryRequest), "requested.memory")
		}, 2*time.Minute, 15*time.Second).Should(Succeed())
	})
})
