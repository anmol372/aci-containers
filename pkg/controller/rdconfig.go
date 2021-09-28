// Copyright 2019 Cisco Systems, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRATIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// creates snat crs.

package controller

import (
	"context"
	"os"
	"reflect"
	"runtime"

	rdConfigv1 "github.com/noironetworks/aci-containers/pkg/rdconfig/apis/aci.snat/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (cont *AciController) syncRdConfig() bool {
	_, file, no, ok := runtime.Caller(1)
	if ok {
		cont.log.Debugf("called from %s#%d\n", file, no)
	}
	cont.log.Debug("Syncing RdConfig")
	var options metav1.GetOptions
	var discoveredSubnets []string
	cont.indexMutex.Lock()
	for _, v := range cont.apicConn.CachedSubnetDns {
		discoveredSubnets = append(discoveredSubnets, v)
	}
	cont.indexMutex.Unlock()
	env := cont.env.(*K8sEnvironment)
	rdConfigClient := env.rdConfigClient
	cont.log.Debugf("RdConfig Client: %#v", rdConfigClient)
	if rdConfigClient == nil {
		return false
	}
	ns := os.Getenv("ACI_SNAT_NAMESPACE")
	name := os.Getenv("ACI_RDCONFIG_NAME")
	cont.log.Debugf("RdConfig name: %s, namespace: %s", name, ns)
	rdConfig, err := rdConfigClient.AciV1().RdConfigs(ns).Get(context.TODO(), name, options)
	if err != nil {
		cont.log.Debugf("Unable to get RDConfig: %s, err: %v", name, err)
		if apierrors.IsNotFound(err) {
			rdConfigInstance := &rdConfigv1.RdConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: ns,
				},
				Spec: rdConfigv1.RdConfigSpec{
					DiscoveredSubnets: discoveredSubnets,
				},
			}
			_, err = rdConfigClient.AciV1().RdConfigs(ns).Create(context.TODO(), rdConfigInstance, metav1.CreateOptions{})
			if err != nil {
				cont.log.Debugf("Unable to create RDConfig: %s, err: %v", name, err)
				return true
			}
			cont.log.Debugf("RdConfig Created: ", rdConfigInstance, err)
		}
	} else {
		cont.log.Debugf("Comparing existing rdconfig DiscoveredSubnets with cached values")
		if !reflect.DeepEqual(rdConfig.Spec.DiscoveredSubnets, discoveredSubnets) {
			rdConfig.Spec.DiscoveredSubnets = discoveredSubnets
			_, err = rdConfigClient.AciV1().RdConfigs(ns).Update(context.TODO(), rdConfig, metav1.UpdateOptions{})
			if err != nil {
				cont.log.Debugf("Unable to Update RDConfig: %s, err: %v", name, err)
				return true
			}
			cont.log.Debug("RdConfig Updated: ", discoveredSubnets)
		}
	}
	return false
}
