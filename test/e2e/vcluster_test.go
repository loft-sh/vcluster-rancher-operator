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
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/loft-sh/vcluster-rancher-operator/test/utils"
)

var _ = Describe("VCluster Helm Driver", Ordered, func() {
	const (
		vclusterName = "test-vc"
		vclusterNS   = "e2e-test-vc"
	)

	var serviceUID string

	BeforeAll(func() {
		By("creating test namespace")
		createNamespace(vclusterNS)
	})

	AfterAll(func() {
		By("deleting vcluster")
		cmd := exec.Command("vcluster", "delete", vclusterName, "--namespace", vclusterNS)
		_, _ = utils.Run(cmd)

		By("deleting test namespace")
		_ = hostClient.CoreV1().Namespaces().Delete(
			context.Background(), vclusterNS, metav1.DeleteOptions{},
		)
	})

	It("should create a Rancher cluster that becomes Active", func() {
		By("creating a vcluster with the helm driver")
		cmd := exec.Command("vcluster", "create", vclusterName,
			"--driver", "helm",
			"--namespace", vclusterNS,
			"--connect=false",
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

		By("waiting for the operator to create a provisioning cluster in Rancher")
		Eventually(func(g Gomega) {
			list, err := rancherClient.Resource(provisioningClustersGVR).Namespace("").List(
				context.Background(),
				metav1.ListOptions{LabelSelector: fmt.Sprintf("loft.sh/vcluster-service-uid=%s", serviceUID)},
			)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(list.Items).NotTo(BeEmpty())
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for the Rancher management cluster to become Active (Ready=True)")
		Eventually(func(g Gomega) {
			list, err := rancherClient.Resource(managementClustersGVR).List(
				context.Background(),
				metav1.ListOptions{LabelSelector: fmt.Sprintf("loft.sh/vcluster-service-uid=%s", serviceUID)},
			)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(list.Items).NotTo(BeEmpty())
			g.Expect(isReady(list.Items[0])).To(BeTrue(),
				"management cluster conditions: %v", conditionSummary(list.Items[0]))
		}, 10*time.Minute, 15*time.Second).Should(Succeed())
	})
})
