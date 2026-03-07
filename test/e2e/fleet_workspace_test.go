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

	"github.com/loft-sh/vcluster-rancher-operator/pkg/constants"
	"github.com/loft-sh/vcluster-rancher-operator/pkg/unstructured/gvk"
	"github.com/loft-sh/vcluster-rancher-operator/test/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	operatorNamespace = "vcluster-rancher-operator-system"
	operatorRelease   = "vcluster-rancher-operator"
)

var _ = Describe("VCluster Fleet Workspace - Auto Create", Ordered, Serial, func() {
	const (
		vclusterName     = "test-vc-missing-ws"
		vclusterNS       = "e2e-test-missing-ws"
		missingWorkspace = "fleet-does-not-exist"
	)

	var serviceUID string

	BeforeAll(func() {
		By("creating test namespace")
		createNamespace(vclusterNS)

		By("reinstalling operator targeting a non-existent fleet workspace with autoCreateWorkspace=true")
		cmd := exec.Command("helm", "upgrade", "--install", operatorRelease, "./chart",
			"--namespace", operatorNamespace,
			"--set", "image.tag=e2e",
			"--set", fmt.Sprintf("fleet.defaultWorkspace=%s", missingWorkspace),
			"--set", "fleet.autoCreateWorkspace=true",
			"--wait",
		)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		By("deleting vcluster")
		cmd := exec.Command("vcluster", "delete", vclusterName, "--namespace", vclusterNS)
		_, _ = utils.Run(cmd)

		By("deleting test namespace")
		_ = hostClient.CoreV1().Namespaces().Delete(
			context.Background(), vclusterNS, metav1.DeleteOptions{},
		)

		By("deleting auto-created fleet workspace")
		workspace, err := rancherClient.Get(context.Background(), gvk.FleetWorkspaceManagementCattle, missingWorkspace, "")
		if err == nil {
			_ = rancherClient.Delete(context.Background(), &workspace)
		}

		By("restoring operator with default fleet workspace")
		cmd = exec.Command("helm", "upgrade", "--install", operatorRelease, "./chart",
			"--namespace", operatorNamespace,
			"--set", "image.tag=e2e",
			"--wait",
		)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})

	It("should auto-create the fleet workspace and create a provisioning cluster", func() {
		By("creating a vcluster")
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

		By("waiting for the operator to auto-create the missing fleet workspace")
		Eventually(func(g Gomega) {
			ws, err := rancherClient.Get(context.Background(), gvk.FleetWorkspaceManagementCattle, missingWorkspace, "")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(ws.GetLabels()).To(HaveKeyWithValue(constants.LabelAutoCreated, "true"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("waiting for the operator to create a provisioning cluster in the new workspace")
		Eventually(func(g Gomega) {
			_, err := rancherClient.GetFirstWithLabel(context.Background(), gvk.ClusterProvisioningCattle, constants.LabelVClusterServiceUID, serviceUID)
			g.Expect(err).NotTo(HaveOccurred())
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})
})
