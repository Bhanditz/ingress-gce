/*
Copyright 2017 The Kubernetes Authors.

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

package app

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/golang/glog"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/kubernetes/pkg/cloudprovider"
	"k8s.io/kubernetes/pkg/cloudprovider/providers/gce"
)

const (
	// Sleep interval to retry cloud client creation.
	cloudClientRetryInterval = 10 * time.Second
)

func NewKubeClient() (kubernetes.Interface, error) {
	var err error
	var config *rest.Config

	if Flags.InCluster {
		if config, err = rest.InClusterConfig(); err != nil {
			return nil, err
		}
	} else {
		if Flags.APIServerHost == "" {
			return nil, fmt.Errorf("please specify the api server address using the flag --apiserver-host")
		}

		config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: Flags.KubeConfigFile},
			&clientcmd.ConfigOverrides{
				ClusterInfo: clientcmdapi.Cluster{
					Server: Flags.APIServerHost,
				},
			}).ClientConfig()
		if err != nil {
			glog.Fatalf("error creating client configuration: %v", err)
		}
	}

	return kubernetes.NewForConfig(config)
}

func NewGCEClient(config io.Reader) *gce.GCECloud {
	getConfigReader := func() io.Reader { return nil }

	if config != nil {
		allConfig, err := ioutil.ReadAll(config)
		if err != nil {
			glog.Fatalf("Error while reading entire config: %v", err)
		}
		glog.V(2).Infof("Using cloudprovider config file:\n%v ", string(allConfig))

		getConfigReader = func() io.Reader {
			return bytes.NewReader(allConfig)
		}
	} else {
		glog.V(2).Infof("No cloudprovider config file provided, using default values.")
	}

	// Creating the cloud interface involves resolving the metadata server to get
	// an oauth token. If this fails, the token provider assumes it's not on GCE.
	// No errors are thrown. So we need to keep retrying till it works because
	// we know we're on GCE.
	for {
		cloudInterface, err := cloudprovider.GetCloudProvider("gce", getConfigReader())
		if err == nil {
			cloud := cloudInterface.(*gce.GCECloud)

			// If this controller is scheduled on a node without compute/rw
			// it won't be allowed to list backends. We can assume that the
			// user has no need for Ingress in this case. If they grant
			// permissions to the node they will have to restart the controller
			// manually to re-create the client.
			// TODO: why do we bail with success out if there is a permission error???
			if _, err = cloud.ListGlobalBackendServices(); err == nil || utils.IsHTTPErrorCode(err, http.StatusForbidden) {
				return cloud
			}
			glog.Warningf("Failed to list backend services, retrying: %v", err)
		} else {
			glog.Warningf("Failed to retrieve cloud interface, retrying: %v", err)
		}
		time.Sleep(cloudClientRetryInterval)
	}
}