/*
Copyright 2019 The KubeEdge Authors.

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
package hubtest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"

	"github.com/kubeedge/kubeedge/tests/e2e/utils"
	. "github.com/kubeedge/kubeedge/tests/performance/common"
)

// configs across the package
var (
	ctx              *utils.TestContext
	cfg              utils.Config
	cloudHubURL      string
	wsscloudHubURL   string
	quiccloudHubURL  string
	controllerHubURL string
)

func TestKubeEdgeK8SDeployment(t *testing.T) {
	// Init params
	var podlist v1.PodList
	RegisterFailHandler(Fail)

	// Init suite
	var _ = BeforeSuite(func() {
		// Init config
		utils.Infof("KubeEdge hub performance test begin!")
		cfg = utils.LoadConfig()
		ctx = utils.NewTestContext(cfg)

		//apply label to all cluster nodes, use the selector to deploy all edgenodes to cluster nodes
		err := ApplyLabel(ctx.Cfg.K8SMasterForProvisionEdgeNodes + NodeHandler)
		Expect(err).Should(BeNil())

		cloudPartHostIP := "192.168.27.23"
		for _, pod := range podlist.Items {
			if strings.Contains(pod.Name, "cloudcore") {
				cloudPartHostIP = "192.168.27.23"
				break
			}
		}

		// Check if KubeEdge Cloud Part is running
		utils.CheckPodRunningState(ctx.Cfg.K8SMasterForKubeEdge+AppHandler, podlist)
		time.Sleep(5 * time.Second)
		CloudCoreDeployment = "cloudcore"
		// Create NodePort Service for KubeEdge Cloud Part
		err = utils.ExposeCloudService(CloudCoreDeployment, ctx.Cfg.K8SMasterForKubeEdge+ServiceHandler)
		Expect(err).Should(BeNil())

		// Get NodePort Service to access KubeEdge Cloud Part from KubeEdge Edge Nodes
		wsPort, quicNodePort := utils.GetServicePort(CloudCoreDeployment, ctx.Cfg.K8SMasterForKubeEdge+ServiceHandler)
		quiccloudHubURL = fmt.Sprintf("%s:%d", cloudPartHostIP, quicNodePort)
		wsscloudHubURL = fmt.Sprintf("wss://%s:%d", cloudPartHostIP, wsPort)
		if IsQuicProtocol {
			cloudHubURL = quiccloudHubURL
		} else {
			cloudHubURL = wsscloudHubURL
		}
		controllerHubURL = fmt.Sprintf("http://%s:%d", cloudPartHostIP, ctx.Cfg.ControllerStubPort)
	})
	AfterSuite(func() {
		By("KubeEdge hub performance test end!")
		// Delete KubeEdge Cloud Part deployment
		//DeleteCloudDeployment(ctx.Cfg.K8SMasterForKubeEdge)
		// Check if KubeEdge Cloud Part is deleted
		//utils.CheckPodDeleteState(ctx.Cfg.K8SMasterForKubeEdge+AppHandler, podlist)
	})
	RunSpecs(t, "KubeEdge hub performance test suite")
}
