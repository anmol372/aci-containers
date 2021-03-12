// Copyright 2018 Cisco Systems, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Handlers for network policy updates.  Generate ACI security groups
// based on Kubernetes network policies.

package util

import (
	"context"
	v1netpol "github.com/noironetworks/aci-containers/pkg/networkpolicy/apis/netpolicy/v1"
	v1netpolclset "github.com/noironetworks/aci-containers/pkg/networkpolicy/clientset/versioned"
	v1net "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func GetNetPolPolicyTypes(indexer cache.Indexer, key string) []v1net.PolicyType {
	npobj, exists, err := indexer.GetByKey(key)
	if !exists || err != nil {
		return nil
	}
	np := npobj.(*v1netpol.NetworkPolicy)
	if len(np.Spec.PolicyTypes) > 0 {
		return np.Spec.PolicyTypes
	}
	if len(np.Spec.Egress) > 0 {
		return []v1net.PolicyType{
			v1net.PolicyTypeIngress,
			v1net.PolicyTypeEgress,
		}
	} else {
		return []v1net.PolicyType{v1net.PolicyTypeIngress}
	}
}

// CreateNodeInfoCR Creates a NodeInfo CR
func CreateNetPol(c *v1netpolclset.Clientset, netpol *v1netpol.NetworkPolicy) error {
	_, err := c.AciV1().NetworkPolicies().Create(context.TODO(), netpol, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	return nil
}

func UpdateNetPol(c *v1netpolclset.Clientset, netpol *v1netpol.NetworkPolicy) error {
	_, err := c.AciV1().NetworkPolicies().Update(context.TODO(), netpol, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return nil
}

func DeleteNetPol(c *v1netpolclset.Clientset, name string) error {
	err := c.AciV1().NetworkPolicies().Delete(context.TODO(), name, metav1.DeleteOptions{})
	if err != nil {
		return err
	}
	return nil
}
