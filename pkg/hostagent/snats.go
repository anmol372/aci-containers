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

// Handlers for snat updates.

package hostagent

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	snatglobal "github.com/noironetworks/aci-containers/pkg/snatglobalinfo/apis/aci.snat/v1"
	snatglobalclset "github.com/noironetworks/aci-containers/pkg/snatglobalinfo/clientset/versioned"
	snatpolicy "github.com/noironetworks/aci-containers/pkg/snatpolicy/apis/aci.snat/v1"
	snatpolicyclset "github.com/noironetworks/aci-containers/pkg/snatpolicy/clientset/versioned"
	"github.com/noironetworks/aci-containers/pkg/util"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/controller"
)

// Filename used to create external service file on host
// example snat-external.service
const SnatService = "snat-external"

type ResourceType int

const (
	POD ResourceType = 1 << iota
	SERVICE
	DEPLOYMENT
	NAMESPACE
	CLUSTER
	INVALID
)

type OpflexPortRange struct {
	Start int `json:"start,omitempty"`
	End   int `json:"end,omitempty"`
}

// This structure is to write the  SnatFile
type OpflexSnatIp struct {
	Uuid          string                   `json:"uuid"`
	InterfaceName string                   `json:"interface-name,omitempty"`
	SnatIp        string                   `json:"snat-ip,omitempty"`
	InterfaceMac  string                   `json:"interface-mac,omitempty"`
	Local         bool                     `json:"local,omitempty"`
	DestIpAddress []string                 `json:"dest,omitempty"`
	PortRange     []OpflexPortRange        `json:"port-range,omitempty"`
	InterfaceVlan uint                     `json:"interface-vlan,omitempty"`
	Zone          uint                     `json:"zone,omitempty"`
	Remote        []OpflexSnatIpRemoteInfo `json:"remote,omitempty"`
}

// This Structure is to calculate remote Info
type OpflexSnatIpRemoteInfo struct {
	NodeIp     string            `json:"snat_ip,omitempty"`
	MacAddress string            `json:"mac,omitempty"`
	PortRange  []OpflexPortRange `json:"port-range,omitempty"`
	Refcount   int               `json:"ref,omitempty"`
}

type opflexSnatGlobalInfo struct {
	SnatIp         string
	MacAddress     string
	PortRange      []OpflexPortRange
	SnatIpUid      string
	SnatPolicyName string
}

type opflexSnatLocalInfo struct {
	Existing     bool                      //True when existing snat-uuids in ep file is to be maintained
	Snatpolicies map[ResourceType][]string //Each resource can represent multiple entries
	PlcyUuids    []string                  //sorted policy uuids
}

func (agent *HostAgent) initSnatGlobalInformerFromClient(
	snatClient *snatglobalclset.Clientset) {
	agent.initSnatGlobalInformerBase(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				obj, err := snatClient.AciV1().SnatGlobalInfos(metav1.NamespaceAll).List(context.TODO(), options)
				if err != nil {
					agent.log.Fatalf("Failed to list SnatGlobalInfo during initialization of SnatGlobalInformer: %s", err)
				}
				return obj, err
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				obj, err := snatClient.AciV1().SnatGlobalInfos(metav1.NamespaceAll).Watch(context.TODO(), options)
				if err != nil {
					agent.log.Fatalf("Failed to watch SnatGlobalInfo during initialization of SnatGlobalInformer: %s", err)
				}
				return obj, err
			},
		})
}

func (agent *HostAgent) initSnatPolicyInformerFromClient(
	snatClient *snatpolicyclset.Clientset) {
	agent.initSnatPolicyInformerBase(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				obj, err := snatClient.AciV1().SnatPolicies().List(context.TODO(), options)
				if err != nil {
					agent.log.Fatalf("Failed to list SnatPolicies during initialization of SnatPolicyInformer: %s", err)
				}
				return obj, err
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				obj, err := snatClient.AciV1().SnatPolicies().Watch(context.TODO(), options)
				if err != nil {
					agent.log.Fatalf("Failed to watch SnatPolicies during initialization of SnatPolicyInformer: %s", err)
				}
				return obj, err
			},
		})
}

func writeSnat(snatfile string, snat *OpflexSnatIp) (bool, error) {
	newdata, err := json.MarshalIndent(snat, "", "  ")
	if err != nil {
		return true, err
	}
	existingdata, err := os.ReadFile(snatfile)
	if err == nil && reflect.DeepEqual(existingdata, newdata) {
		return false, nil
	}

	err = os.WriteFile(snatfile, newdata, 0644)
	return true, err
}

func (agent *HostAgent) FormSnatFilePath(uuid string) string {
	return filepath.Join(agent.config.OpFlexSnatDir, uuid+".snat")
}

func SnatGlobalInfoLogger(log *logrus.Logger, snat *snatglobal.SnatGlobalInfo) *logrus.Entry {
	return log.WithFields(logrus.Fields{
		"namespace": snat.ObjectMeta.Namespace,
		"name":      snat.ObjectMeta.Name,
		"spec":      snat.Spec,
	})
}

func opflexSnatIpLogger(log *logrus.Logger, snatip *OpflexSnatIp) *logrus.Entry {
	return log.WithFields(logrus.Fields{
		"uuid":           snatip.Uuid,
		"snat_ip":        snatip.SnatIp,
		"mac_address":    snatip.InterfaceMac,
		"port_range":     snatip.PortRange,
		"local":          snatip.Local,
		"interface-name": snatip.InterfaceName,
		"interfcae-vlan": snatip.InterfaceVlan,
		"remote":         snatip.Remote,
	})
}

func (agent *HostAgent) initSnatGlobalInformerBase(listWatch *cache.ListWatch) {
	agent.snatGlobalInformer = cache.NewSharedIndexInformer(
		listWatch,
		&snatglobal.SnatGlobalInfo{},
		controller.NoResyncPeriodFunc(),
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)
	agent.snatGlobalInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			agent.snatGlobalInfoUpdate(obj)
		},
		UpdateFunc: func(_ interface{}, obj interface{}) {
			agent.snatGlobalInfoUpdate(obj)
		},
		DeleteFunc: func(obj interface{}) {
			agent.snatGlobalInfoDelete(obj)
		},
	})
	agent.log.Info("Initializing SnatGlobal Info Informers")
}

func (agent *HostAgent) initSnatPolicyInformerBase(listWatch *cache.ListWatch) {
	agent.snatPolicyInformer = cache.NewSharedIndexInformer(
		listWatch,
		&snatpolicy.SnatPolicy{}, controller.NoResyncPeriodFunc(),
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)
	agent.snatPolicyInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			agent.snatPolicyAdded(obj)
		},
		UpdateFunc: func(oldobj interface{}, newobj interface{}) {
			agent.snatPolicyUpdated(oldobj, newobj)
		},
		DeleteFunc: func(obj interface{}) {
			agent.snatPolicyDeleted(obj)
		},
	})
	agent.log.Infof("Initializing Snat Policy Informers")
}

func (agent *HostAgent) snatPolicyAdded(obj interface{}) {
	agent.indexMutex.Lock()
	defer agent.indexMutex.Unlock()
	agent.snatPolicyCacheMutex.Lock()
	defer agent.snatPolicyCacheMutex.Unlock()
	policyinfo := obj.(*snatpolicy.SnatPolicy)
	agent.log.Infof("Snat Policy Added: name=%s", policyinfo.ObjectMeta.Name)
	if policyinfo.Status.State != snatpolicy.Ready {
		return
	}
	agent.snatPolicyCache[policyinfo.ObjectMeta.Name] = policyinfo
	setDestIp(agent.snatPolicyCache[policyinfo.ObjectMeta.Name].Spec.DestIp)
	agent.handleSnatUpdate(policyinfo)
}

func (agent *HostAgent) snatPolicyUpdated(oldobj, newobj interface{}) {
	agent.indexMutex.Lock()
	defer agent.indexMutex.Unlock()
	agent.snatPolicyCacheMutex.Lock()
	defer agent.snatPolicyCacheMutex.Unlock()
	oldpolicyinfo := oldobj.(*snatpolicy.SnatPolicy)
	newpolicyinfo := newobj.(*snatpolicy.SnatPolicy)
	agent.log.Infof("Snat Policy Updated: name=%s, policy status=%s", newpolicyinfo.ObjectMeta.Name, newpolicyinfo.Status.State)
	if reflect.DeepEqual(oldpolicyinfo, newpolicyinfo) {
		return
	}
	//1. check if the local nodename is  present in globalinfo
	// 2. if it is not present then delete the policy from localInfo as the portinfo is not allocated  for node
	if newpolicyinfo.Status.State == snatpolicy.IpPortsExhausted {
		agent.log.Infof("Ports exhausted for snat policy: %s", newpolicyinfo.ObjectMeta.Name)
		return
	}
	if newpolicyinfo.Status.State != snatpolicy.Ready {
		return
	}
	agent.snatPolicyCache[newpolicyinfo.ObjectMeta.Name] = newpolicyinfo
	setDestIp(agent.snatPolicyCache[newpolicyinfo.ObjectMeta.Name].Spec.DestIp)
	// After Validation of SnatPolicy State will be set to Ready
	if newpolicyinfo.Status.State != oldpolicyinfo.Status.State {
		agent.handleSnatUpdate(newpolicyinfo)
		return
	}
	update := true
	if !reflect.DeepEqual(oldpolicyinfo.Spec.Selector,
		newpolicyinfo.Spec.Selector) {
		var poduids []string
		// delete all the pods matching the policy
		for uuid, res := range agent.snatPods[newpolicyinfo.ObjectMeta.Name] {
			agent.deleteSnatLocalInfo(uuid, res, newpolicyinfo.ObjectMeta.Name)
			poduids = append(poduids, uuid)
		}
		agent.updateEpFiles(poduids)
		agent.handleSnatUpdate(newpolicyinfo)
		var matchingpods []string
		for uuid := range agent.snatPods[newpolicyinfo.ObjectMeta.Name] {
			matchingpods = append(matchingpods, uuid)
		}
		// Nodeinfo trigger handles if handlesnatUpdate don't match any pods
		// this trigger clears the globalinfo allocated for node
		if len(poduids) > 0 && len(matchingpods) == 0 {
			agent.scheduleSyncNodeInfo()
		} else {
			// Epfiles needs to be updated if there are any  pods matching with newlabel
			agent.updateEpFiles(matchingpods)
		}
		update = false
	}
	// destination update can be ignored  if labels also changed
	if !reflect.DeepEqual(oldpolicyinfo.Spec.DestIp,
		newpolicyinfo.Spec.DestIp) && update {
		// updateEpFile
		// SyncSnatFile
		var poduids []string
		for uid := range agent.snatPods[newpolicyinfo.ObjectMeta.Name] {
			poduids = append(poduids, uid)
		}
		// This EPfiles update based on destip chanes policy order is maintained
		agent.updateEpFiles(poduids)
		agent.scheduleSyncSnats()
	}
}

func (agent *HostAgent) snatPolicyDeleted(obj interface{}) {
	agent.indexMutex.Lock()
	defer agent.indexMutex.Unlock()
	agent.snatPolicyCacheMutex.Lock()
	defer agent.snatPolicyCacheMutex.Unlock()
	policyinfo, isSnatPolicy := obj.(*snatpolicy.SnatPolicy)
	if !isSnatPolicy {
		deletedState, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			agent.log.Error("Received unexpected object: ", obj)
			return
		}
		policyinfo, ok = deletedState.Obj.(*snatpolicy.SnatPolicy)
		if !ok {
			agent.log.Error("DeletedFinalStateUnknown contained non-Snatpolicy object: ", deletedState.Obj)
			return
		}
	}
	agent.log.Infof("Snat Policy Deleted: name=%s", policyinfo.ObjectMeta.Name)
	agent.deletePolicy(policyinfo)
	delete(agent.snatPolicyCache, policyinfo.ObjectMeta.Name)
}

func (agent *HostAgent) handleSnatUpdate(policy *snatpolicy.SnatPolicy) {
	// First Parse the policy and check for applicability
	// list all the Pods based on labels and namespace
	agent.log.Debug("Handle snatUpdate: ", policy)
	_, err := cache.MetaNamespaceKeyFunc(policy)
	if err != nil {
		return
	}
	// 1.List the targets matching the policy based on policy config
	uids := make(map[ResourceType][]string)
	switch {
	case len(policy.Spec.SnatIp) == 0:
		//handle policy for service pods
		var services []*v1.Service
		var poduids []string
		selector := labels.SelectorFromSet(policy.Spec.Selector.Labels)
		cache.ListAll(agent.serviceInformer.GetIndexer(), selector,
			func(servobj interface{}) {
				services = append(services, servobj.(*v1.Service))
			})
		// list the pods and apply the policy at service target
		for _, service := range services {
			uids, _ := agent.getPodsMatchingObject(service, policy.ObjectMeta.Name)
			poduids = append(poduids, uids...)
			key, err := agent.MetaNamespaceUIDFunc(service)
			if err == nil {
				_, ok := agent.ReadSnatPolicyLabel(key)
				if ok && len(policy.Spec.Selector.Labels) > 0 {
					agent.WriteSnatPolicyLabel(key, policy.ObjectMeta.Name, SERVICE)
				}
			}
		}
		uids[SERVICE] = poduids
	case reflect.DeepEqual(policy.Spec.Selector, snatpolicy.PodSelector{}):
		// This Policy will be applied at cluster level
		var poduids []string
		// handle policy for cluster
		for k := range agent.opflexEps {
			poduids = append(poduids, k)
		}
		uids[CLUSTER] = poduids
	case len(policy.Spec.Selector.Labels) == 0:
		// This is namespace based policy
		var poduids []string
		cache.ListAllByNamespace(agent.podInformer.GetIndexer(), policy.Spec.Selector.Namespace, labels.Everything(),
			func(podobj interface{}) {
				pod := podobj.(*v1.Pod)
				if pod.Spec.NodeName == agent.config.NodeName {
					poduids = append(poduids, string(pod.ObjectMeta.UID))
				}
			})
		uids[NAMESPACE] = poduids
	default:
		poduids, deppoduids, nspoduids :=
			agent.getPodUidsMatchingLabel(policy.Spec.Selector.Namespace, policy.Spec.Selector.Labels, policy.ObjectMeta.Name)
		uids[POD] = poduids
		uids[DEPLOYMENT] = deppoduids
		uids[NAMESPACE] = nspoduids
	}
	succeededPodUids := agent.getPodUidsSucceeded()
	for res, poduids := range uids {
		if len(succeededPodUids) > 0 {
			poduids = difference(poduids, succeededPodUids)
		}
		agent.applyPolicy(poduids, res, policy.GetName())
	}
}

func (agent *HostAgent) getPodUidsSucceeded() (poduids []string) {
	cache.ListAll(agent.nsInformer.GetIndexer(), labels.Everything(),
		func(nsobj interface{}) {
			poduids = append(poduids, agent.getSucceededPodsUids(nsobj)...)
		})
	return
}

func (agent *HostAgent) getSucceededPodsUids(obj interface{}) (poduids []string) {
	ns, _ := obj.(*v1.Namespace)
	cache.ListAllByNamespace(agent.podInformer.GetIndexer(),
		ns.ObjectMeta.Name, labels.Everything(),
		func(podobj interface{}) {
			pod := podobj.(*v1.Pod)
			if pod.Spec.NodeName == agent.config.NodeName && pod.Status.Phase == v1.PodSucceeded {
				poduids = append(poduids, string(pod.ObjectMeta.UID))
			}
		})
	if len(poduids) != 0 {
		agent.log.Info("Matching succeeded pod uids: ", poduids)
	}
	return
}

func (agent *HostAgent) updateSnatPolicyLabels(obj interface{}, policyname string) (poduids []string) {
	uids, res := agent.getPodsMatchingObject(obj, policyname)
	if len(uids) > 0 {
		key, _ := agent.MetaNamespaceUIDFunc(obj)
		if _, ok := agent.ReadSnatPolicyLabel(key); ok {
			agent.WriteSnatPolicyLabel(key, policyname, res)
		}
	}
	return uids
}

// Get all the pods matching the Policy Selector
func (agent *HostAgent) getPodUidsMatchingLabel(namespace string, label map[string]string, policyname string) (poduids []string,
	deppoduids []string, nspoduids []string) {
	selector := labels.SelectorFromSet(label)
	cache.ListAll(agent.podInformer.GetIndexer(), selector,
		func(podobj interface{}) {
			pod := podobj.(*v1.Pod)
			if pod.Spec.NodeName == agent.config.NodeName {
				poduids = append(poduids, agent.updateSnatPolicyLabels(podobj, policyname)...)
			}
		})
	cache.ListAll(agent.depInformer.GetIndexer(), selector,
		func(depobj interface{}) {
			deppoduids = append(deppoduids, agent.updateSnatPolicyLabels(depobj, policyname)...)
		})
	cache.ListAll(agent.nsInformer.GetIndexer(), selector,
		func(nsobj interface{}) {
			nspoduids = append(nspoduids, agent.updateSnatPolicyLabels(nsobj, policyname)...)
		})
	return
}

// Apply the Policy at Resource level
func (agent *HostAgent) applyPolicy(poduids []string, res ResourceType, snatPolicyName string) {
	nodeUpdate := false
	if len(poduids) == 0 {
		return
	}
	if _, ok := agent.snatPods[snatPolicyName]; !ok {
		agent.snatPods[snatPolicyName] = make(map[string]ResourceType)
		nodeUpdate = true
	}
	for _, uid := range poduids {
		_, ok := agent.opflexSnatLocalInfos[uid]
		if !ok {
			var localinfo opflexSnatLocalInfo
			localinfo.Snatpolicies = make(map[ResourceType][]string)
			localinfo.Snatpolicies[res] = append(localinfo.Snatpolicies[res], snatPolicyName)
			agent.opflexSnatLocalInfos[uid] = &localinfo
			agent.snatPods[snatPolicyName][uid] |= res
			agent.log.Debug("Apply policy res: ", agent.snatPods[snatPolicyName][uid])
		} else {
			present := false
			for _, name := range agent.opflexSnatLocalInfos[uid].Snatpolicies[res] {
				if name == snatPolicyName {
					present = true
				}
			}
			if !present {
				agent.opflexSnatLocalInfos[uid].Snatpolicies[res] =
					append(agent.opflexSnatLocalInfos[uid].Snatpolicies[res], snatPolicyName)
				agent.snatPods[snatPolicyName][uid] |= res
				agent.log.Debug("Apply policy res: ", agent.snatPods[snatPolicyName][uid])
			}
		}
	}
	if nodeUpdate {
		agent.log.Debug("Schedule the node Sync")
		agent.scheduleSyncNodeInfo()
	} else {
		// trigger update  the epfile
		agent.updateEpFiles(poduids)
	}
}

// Sync the NodeInfo
func (agent *HostAgent) syncSnatNodeInfo() bool {
	if !agent.syncEnabled {
		return false
	}
	snatPolicyNames := make(map[string]bool)
	agent.indexMutex.Lock()
	for key, val := range agent.snatPods {
		if len(val) > 0 {
			snatPolicyNames[key] = true
		}
	}
	agent.indexMutex.Unlock()
	env := agent.env.(*K8sEnvironment)
	if env == nil {
		return false
	}
	// send nodeupdate for the policy names
	if !agent.InformNodeInfo(env.nodeInfo, snatPolicyNames) {
		agent.log.Debug("Failed to update retry: ", snatPolicyNames)
		return true
	}
	agent.log.Debug("Updated Node Info: ", snatPolicyNames)
	return false
}

func (agent *HostAgent) deletePolicy(policy *snatpolicy.SnatPolicy) {
	pods, ok := agent.snatPods[policy.GetName()]
	var poduids []string
	if !ok {
		return
	}
	for uuid, res := range pods {
		agent.deleteSnatLocalInfo(uuid, res, policy.GetName())
		poduids = append(poduids, uuid)
	}
	agent.updateEpFiles(poduids)
	delete(agent.snatPods, policy.GetName())
	agent.log.Infof("SnatPolicy deleted update Nodeinfo: %s", policy.GetName())
	agent.scheduleSyncNodeInfo()
	agent.DeleteMatchingSnatPolicyLabel(policy.GetName())
}

func (agent *HostAgent) deleteSnatLocalInfo(poduid string, res ResourceType, plcyname string) {
	localinfo, ok := agent.opflexSnatLocalInfos[poduid]
	if ok {
		i := uint(0)
		j := uint(0)
		// loop through all the resources matching the policy
		for i < uint(res) {
			i = 1 << j
			j++
			if i&uint(res) == i {
				length := len(localinfo.Snatpolicies[ResourceType(i)])
				deletedcount := 0
				for k := 0; k < length; k++ {
					l := k - deletedcount
					// delete the matching policy from  policy stack
					if plcyname == localinfo.Snatpolicies[ResourceType(i)][l] {
						agent.log.Infof("Delete the Snat Policy name from SnatLocalInfo: %s", plcyname)
						localinfo.Snatpolicies[ResourceType(i)] =
							append(localinfo.Snatpolicies[ResourceType(i)][:l],
								localinfo.Snatpolicies[ResourceType(i)][l+1:]...)
						deletedcount++
					}
				}
				agent.log.Debug("Opflex agent and localinfo: ", agent.opflexSnatLocalInfos[poduid], localinfo)
				if len(localinfo.Snatpolicies[res]) == 0 {
					delete(localinfo.Snatpolicies, res)
				}
				if v, ok := agent.snatPods[plcyname]; ok {
					if _, ok := v[poduid]; ok {
						agent.snatPods[plcyname][poduid] &= ^(res) // clear the bit
						if agent.snatPods[plcyname][poduid] == 0 { // delete the pod if no resource is pointing for the policy
							delete(agent.snatPods[plcyname], poduid)
							if len(agent.snatPods[plcyname]) == 0 {
								delete(agent.snatPods, plcyname)
							}
						}
					}
				}
			}
		}
	}
}

func (agent *HostAgent) snatGlobalInfoUpdate(obj interface{}) {
	agent.indexMutex.Lock()
	defer agent.indexMutex.Unlock()
	snat := obj.(*snatglobal.SnatGlobalInfo)
	key, err := cache.MetaNamespaceKeyFunc(snat)
	if err != nil {
		SnatGlobalInfoLogger(agent.log, snat).
			Error("Could not create key:" + err.Error())
		return
	}
	agent.log.Info("Snat Global Object Added/Updated: ", snat)
	agent.doUpdateSnatGlobalInfo(key)
}

func (agent *HostAgent) doUpdateSnatGlobalInfo(key string) {
	snatobj, exists, err :=
		agent.snatGlobalInformer.GetStore().GetByKey(key)
	if err != nil {
		agent.log.Error("Could not lookup snat for " +
			key + ": " + err.Error())
		return
	}
	if !exists || snatobj == nil {
		return
	}
	snat := snatobj.(*snatglobal.SnatGlobalInfo)
	logger := SnatGlobalInfoLogger(agent.log, snat)
	agent.snaGlobalInfoChanged(snatobj, logger)
}

func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

func (agent *HostAgent) snaGlobalInfoChanged(snatobj interface{}, logger *logrus.Entry) {
	agent.snatPolicyCacheMutex.RLock()
	defer agent.snatPolicyCacheMutex.RUnlock()
	snat := snatobj.(*snatglobal.SnatGlobalInfo)
	syncSnat := false
	updateLocalInfo := false
	if logger == nil {
		logger = agent.log.WithFields(logrus.Fields{})
	}
	logger.Debug("Snat Global info Changed...")
	globalInfo := snat.Spec.GlobalInfos
	// This case is possible when all the pods will be deleted from that node
	if len(globalInfo) < len(agent.opflexSnatGlobalInfos) {
		for nodename := range agent.opflexSnatGlobalInfos {
			if _, ok := globalInfo[nodename]; !ok {
				delete(agent.opflexSnatGlobalInfos, nodename)
				syncSnat = true
			}
		}
	}
	for nodename, val := range globalInfo {
		var newglobalinfos []*opflexSnatGlobalInfo
		for _, v := range val {
			portrange := make([]OpflexPortRange, 1)
			portrange[0].Start = v.PortRanges[0].Start
			portrange[0].End = v.PortRanges[0].End
			nodeInfo := &opflexSnatGlobalInfo{
				SnatIp:         v.SnatIp,
				MacAddress:     v.MacAddress,
				PortRange:      portrange,
				SnatIpUid:      v.SnatIpUid,
				SnatPolicyName: v.SnatPolicyName,
			}
			newglobalinfos = append(newglobalinfos, nodeInfo)
		}
		existing, ok := agent.opflexSnatGlobalInfos[nodename]
		if (ok && !reflect.DeepEqual(existing, newglobalinfos)) || !ok {
			agent.opflexSnatGlobalInfos[nodename] = newglobalinfos
			if nodename == agent.config.NodeName {
				updateLocalInfo = true
			}
			syncSnat = true
		}
	}

	snatFileName := SnatService + ".service"
	filePath := filepath.Join(agent.config.OpFlexServiceDir, snatFileName)
	file_exists := fileExists(filePath)
	if len(agent.opflexSnatGlobalInfos) > 0 {
		// if more than one global infos, create snat ext file
		as := &opflexService{
			Uuid:              SnatService,
			DomainPolicySpace: agent.config.AciVrfTenant,
			DomainName:        agent.config.AciVrf,
			ServiceMode:       "loadbalancer",
			ServiceMappings:   make([]opflexServiceMapping, 0),
			InterfaceName:     agent.config.UplinkIface,
			InterfaceVlan:     uint16(agent.config.ServiceVlan),
			ServiceMac:        agent.config.UplinkMacAdress,
			InterfaceIp:       agent.serviceEp.Ipv4.String(),
		}
		sm := &opflexServiceMapping{
			Conntrack: true,
		}
		as.ServiceMappings = append(as.ServiceMappings, *sm)
		agent.opflexServices[SnatService] = as
		if !file_exists {
			wrote, err := writeAs(filePath, as)
			if err != nil {
				agent.log.Debug("Unable to write snat ext service file:")
			} else if wrote {
				agent.log.Debug("Created snat ext service file")
			}
		}
	} else {
		delete(agent.opflexServices, SnatService)
		// delete snat service file if no global infos exist
		if file_exists {
			err := os.Remove(filePath)
			if err != nil {
				agent.log.Debug("Unable to delete snat ext service file")
			} else {
				agent.log.Debug("Deleted snat ext service file")
			}
		}
	}
	if syncSnat {
		agent.scheduleSyncSnats()
	}
	if updateLocalInfo {
		var poduids []string
		for _, v := range agent.opflexSnatGlobalInfos[agent.config.NodeName] {
			for uuid := range agent.snatPods[v.SnatPolicyName] {
				poduids = append(poduids, uuid)
			}
		}
		agent.log.Debug("Updating EpFile GlobalInfo Context: ", poduids)
		agent.updateEpFiles(poduids)
	}
}

func (agent *HostAgent) snatGlobalInfoDelete(obj interface{}) {
	agent.log.Debug("Snat Global Info Obj Delete")
	snat := obj.(*snatglobal.SnatGlobalInfo)
	globalInfo := snat.Spec.GlobalInfos
	agent.indexMutex.Lock()
	for nodename := range globalInfo {
		delete(agent.opflexSnatGlobalInfos, nodename)
	}
	agent.indexMutex.Unlock()
}

func (agent *HostAgent) syncSnat() bool {
	if !agent.syncEnabled {
		return false
	}
	agent.log.Debug("Syncing snats")
	agent.indexMutex.Lock()
	opflexSnatIps := make(map[string]*OpflexSnatIp)
	remoteinfo := make(map[string][]OpflexSnatIpRemoteInfo)
	// set the remote info for every snatIp
	for nodename, v := range agent.opflexSnatGlobalInfos {
		for _, ginfo := range v {
			if nodename != agent.config.NodeName {
				var remote OpflexSnatIpRemoteInfo
				remote.MacAddress = ginfo.MacAddress
				remote.PortRange = ginfo.PortRange
				remoteinfo[ginfo.SnatIp] = append(remoteinfo[ginfo.SnatIp], remote)
			}
		}
	}
	agent.log.Debug("Opflex SnatIp RemoteInfo map is: ", remoteinfo)
	// set the Opflex Snat IP information
	localportrange := make(map[string][]OpflexPortRange)
	ginfos, ok := agent.opflexSnatGlobalInfos[agent.config.NodeName]

	if ok {
		for _, ginfo := range ginfos {
			localportrange[ginfo.SnatIp] = ginfo.PortRange
		}
	}

	for _, v := range agent.opflexSnatGlobalInfos {
		for _, ginfo := range v {
			var snatinfo OpflexSnatIp
			// set the local portrange
			snatinfo.InterfaceName = agent.config.UplinkIface
			snatinfo.InterfaceVlan = agent.config.ServiceVlan
			snatinfo.InterfaceMac = agent.config.UplinkMacAdress
			snatinfo.Local = false
			if _, ok := localportrange[ginfo.SnatIp]; ok {
				snatinfo.PortRange = localportrange[ginfo.SnatIp]
				// need to sort the order
				if _, ok := agent.snatPolicyCache[ginfo.SnatPolicyName]; ok {
					if len(agent.snatPolicyCache[ginfo.SnatPolicyName].Spec.DestIp) == 0 {
						snatinfo.DestIpAddress = []string{"0.0.0.0/0"}
					} else {
						snatinfo.DestIpAddress =
							agent.snatPolicyCache[ginfo.SnatPolicyName].Spec.DestIp
					}
				}
				snatinfo.Local = true
			}
			snatinfo.SnatIp = ginfo.SnatIp
			snatinfo.Uuid = ginfo.SnatIpUid
			snatinfo.Zone = agent.config.Zone
			snatinfo.Remote = remoteinfo[ginfo.SnatIp]
			opflexSnatIps[ginfo.SnatIpUid] = &snatinfo
			agent.log.Debug("Opflex Snat data IP: ", opflexSnatIps[ginfo.SnatIpUid])
		}
	}
	agent.indexMutex.Unlock()
	files, err := os.ReadDir(agent.config.OpFlexSnatDir)
	if err != nil {
		agent.log.WithFields(
			logrus.Fields{"SnatDir: ": agent.config.OpFlexSnatDir},
		).Error("Could not read directory " + err.Error())
		return true
	}
	seen := make(map[string]bool)
	for _, f := range files {
		uuid := f.Name()
		if strings.HasSuffix(uuid, ".snat") {
			uuid = uuid[:len(uuid)-5]
		} else {
			continue
		}

		snatfile := filepath.Join(agent.config.OpFlexSnatDir, f.Name())
		logger := agent.log.WithFields(
			logrus.Fields{"Uuid": uuid})
		existing, ok := opflexSnatIps[uuid]
		if ok {
			agent.log.Debugf("snatfile:%s\n", snatfile)
			wrote, err := writeSnat(snatfile, existing)
			if err != nil {
				opflexSnatIpLogger(agent.log, existing).Error("Error writing snat file: ", err)
			} else if wrote {
				opflexSnatIpLogger(agent.log, existing).Info("Updated snat")
			}
			seen[uuid] = true
		} else {
			logger.Info("Removing snat")
			os.Remove(snatfile)
		}
	}
	for _, snat := range opflexSnatIps {
		if seen[snat.Uuid] {
			continue
		}
		opflexSnatIpLogger(agent.log, snat).Info("Adding Snat")
		snatfile :=
			agent.FormSnatFilePath(snat.Uuid)
		_, err = writeSnat(snatfile, snat)
		if err != nil {
			opflexSnatIpLogger(agent.log, snat).
				Error("Error writing snat file: ", err)
		}
	}
	agent.log.Debug("Finished snat sync")
	return false
}

// Get the Pods matching the Object selector
func (agent *HostAgent) getPodsMatchingObject(obj interface{}, policyname string) (poduids []string, res ResourceType) {
	metadata, err := meta.Accessor(obj)
	if err != nil {
		return
	}
	if !agent.isPolicyNameSpaceMatches(policyname, metadata.GetNamespace()) {
		return
	}
	switch obj := obj.(type) {
	case *v1.Pod:
		poduids = append(poduids, string(obj.ObjectMeta.UID))
		if len(poduids) != 0 {
			agent.log.Info("Matching pod uids: ", poduids)
		}
	case *appsv1.Deployment:
		depkey, _ :=
			cache.MetaNamespaceKeyFunc(obj)
		for _, podkey := range agent.depPods.GetPodForObj(depkey) {
			podobj, exists, err := agent.podInformer.GetStore().GetByKey(podkey)
			if err != nil {
				agent.log.Error("Could not lookup pod: ", err)
				continue
			}
			if !exists || podobj == nil {
				agent.log.Error("Object doesn't exist yet ", podkey)
				continue
			}
			poduids = append(poduids, string(podobj.(*v1.Pod).ObjectMeta.UID))
		}
		if len(poduids) != 0 {
			agent.log.Info("Matching deployment pod uids: ", poduids)
		}
		res = DEPLOYMENT
	case *v1.Service:
		selector := labels.SelectorFromSet(obj.Spec.Selector)
		cache.ListAllByNamespace(agent.podInformer.GetIndexer(),
			obj.ObjectMeta.Namespace, selector,
			func(podobj interface{}) {
				pod := podobj.(*v1.Pod)
				if pod.Spec.NodeName == agent.config.NodeName {
					poduids = append(poduids, string(pod.ObjectMeta.UID))
				}
			})
		if len(poduids) != 0 {
			agent.log.Info("Matching service pod uids: ", poduids)
		}
		res = SERVICE
	case *v1.Namespace:
		cache.ListAllByNamespace(agent.podInformer.GetIndexer(),
			obj.ObjectMeta.Name, labels.Everything(),
			func(podobj interface{}) {
				pod := podobj.(*v1.Pod)
				if pod.Spec.NodeName == agent.config.NodeName {
					poduids = append(poduids, string(pod.ObjectMeta.UID))
				}
			})
		if len(poduids) != 0 {
			agent.log.Info("Matching namespace pod uids: ", poduids)
		}
		res = NAMESPACE
	default:
	}
	return
}

// Updates the EPFile with Snatuuid's
func (agent *HostAgent) updateEpFiles(poduids []string) {
	syncEp := false
	for _, uid := range poduids {
		localinfo, ok := agent.opflexSnatLocalInfos[uid]
		if !ok {
			continue
		}
		var i uint = 1
		var pos uint = 0
		var policystack []string
		// 1. loop through all the resource hierarchy
		// 2. Compute the Policy Stack
		for ; i <= uint(CLUSTER); i = 1 << pos {
			pos++
			seen := make(map[string]bool)
			policies, ok := localinfo.Snatpolicies[ResourceType(i)]
			var sortedpolicies []string
			if ok {
				for _, name := range policies {
					if _, ok := seen[name]; !ok {
						seen[name] = true
						sortedpolicies = append(sortedpolicies, name)
					} else {
						continue
					}
				}
				sort.Slice(sortedpolicies,
					func(i, j int) bool {
						return agent.compare(sortedpolicies[i], sortedpolicies[j])
					})
			}
			policystack = append(policystack, sortedpolicies...)
		}
		var uids []string
		for _, name := range policystack {
			for _, val := range agent.opflexSnatGlobalInfos[agent.config.NodeName] {
				if val.SnatPolicyName == name {
					uids = append(uids, val.SnatIpUid)
				}
			}
			if len(agent.snatPolicyCache[name].Spec.DestIp) == 0 {
				break
			}
		}
		if !reflect.DeepEqual(agent.opflexSnatLocalInfos[uid].PlcyUuids, uids) {
			agent.log.Debug("Update EpFile: ", uids)
			agent.opflexSnatLocalInfos[uid].Existing = false
			agent.opflexSnatLocalInfos[uid].PlcyUuids = uids
			if len(uids) == 0 {
				delete(agent.opflexSnatLocalInfos, uid)
			}
			syncEp = true
		}
	}
	if syncEp {
		agent.scheduleSyncEps()
	}
	agent.scheduleSyncLocalInfo()
}

func (agent *HostAgent) compare(plcy1, plcy2 string) bool {
	sort := false
	for _, a := range agent.snatPolicyCache[plcy1].Spec.DestIp {
		ip_temp := net.ParseIP(a)
		if ip_temp != nil && ip_temp.To4() != nil {
			a += "/32"
		}
		for _, b := range agent.snatPolicyCache[plcy2].Spec.DestIp {
			ip_temp := net.ParseIP(b)
			if ip_temp != nil && ip_temp.To4() != nil {
				b += "/32"
			}
			// TODO need to handle if the order is reversed across the policies.
			// order reversing is ideally a wrong config. may be we need to block at verfication level
			if compareIps(a, b) {
				sort = true
			}
		}
	}
	return sort
}

func (agent *HostAgent) getMatchingServices(namespace string, label map[string]string) []*v1.Service {
	var services, matchingServices []*v1.Service
	cache.ListAllByNamespace(agent.serviceInformer.GetIndexer(), namespace, labels.Everything(),
		func(servobj interface{}) {
			services = append(services, servobj.(*v1.Service))
		})
	for _, service := range services {
		if service.Spec.Selector == nil {
			continue
		}
		svcSelector := labels.SelectorFromSet(service.Spec.Selector)
		if svcSelector.Matches(labels.Set(label)) {
			matchingServices = append(matchingServices, service)
		}
	}

	return matchingServices
}

// Must acquire snatPolicyCacheMutex.RLock
func (agent *HostAgent) getMatchingSnatPolicy(obj interface{}) (snatPolicyNames map[string][]ResourceType) {
	snatPolicyNames = make(map[string][]ResourceType)
	_, err := agent.MetaNamespaceUIDFunc(obj)
	if err != nil {
		return
	}
	metadata, err := meta.Accessor(obj)
	if err != nil {
		return
	}
	namespace := metadata.GetNamespace()
	label := metadata.GetLabels()
	name := metadata.GetName()
	res := getResourceType(obj)
	for _, item := range agent.snatPolicyCache {
		// check for empty policy selctor
		if reflect.DeepEqual(item.Spec.Selector, snatpolicy.PodSelector{}) {
			snatPolicyNames[item.ObjectMeta.Name] =
				append(snatPolicyNames[item.ObjectMeta.Name], CLUSTER)
		} else if len(item.Spec.Selector.Labels) == 0 &&
			item.Spec.Selector.Namespace == namespace { // check policy matches namespace
			if res == SERVICE {
				if len(item.Spec.SnatIp) == 0 {
					snatPolicyNames[item.ObjectMeta.Name] =
						append(snatPolicyNames[item.ObjectMeta.Name], SERVICE)
				}
			} else {
				if len(item.Spec.SnatIp) > 0 {
					snatPolicyNames[item.ObjectMeta.Name] =
						append(snatPolicyNames[item.ObjectMeta.Name], NAMESPACE)
				}
			}
		} else { //Check Policy matches the labels on the Object
			if (item.Spec.Selector.Namespace != "" &&
				item.Spec.Selector.Namespace == namespace) ||
				(item.Spec.Selector.Namespace == "") {
				if util.MatchLabels(item.Spec.Selector.Labels, label) {
					snatPolicyNames[item.ObjectMeta.Name] =
						append(snatPolicyNames[item.ObjectMeta.Name], res)
				}
				if res == POD {
					if len(item.Spec.SnatIp) == 0 {
						matchingServices := agent.getMatchingServices(namespace, label)
						agent.log.Debug("Matching services for pod ", name, " : ", matchingServices)
						for _, matchingSvc := range matchingServices {
							if util.MatchLabels(item.Spec.Selector.Labels,
								matchingSvc.ObjectMeta.Labels) {
								snatPolicyNames[item.ObjectMeta.Name] =
									append(snatPolicyNames[item.ObjectMeta.Name], SERVICE)
								break
							}
						}
					} else {
						podKey, _ := cache.MetaNamespaceKeyFunc(obj)
						for _, dpkey := range agent.depPods.GetObjForPod(podKey) {
							depobj, exists, err :=
								agent.depInformer.GetStore().GetByKey(dpkey)
							if err != nil {
								agent.log.Error("Could not lookup snat for " +
									dpkey + ": " + err.Error())
								continue
							}
							if !exists || depobj == nil {
								continue
							}
							if util.MatchLabels(item.Spec.Selector.Labels,
								depobj.(*appsv1.Deployment).ObjectMeta.Labels) {
								snatPolicyNames[item.ObjectMeta.Name] =
									append(snatPolicyNames[item.ObjectMeta.Name], DEPLOYMENT)
							}
						}
						nsobj, exists, err := agent.nsInformer.GetStore().GetByKey(namespace)
						if err != nil {
							agent.log.Error("Could not lookup snat for " +
								namespace + ": " + err.Error())
							continue
						}
						if !exists || nsobj == nil {
							continue
						}
						if util.MatchLabels(item.Spec.Selector.Labels,
							nsobj.(*v1.Namespace).ObjectMeta.Labels) {
							snatPolicyNames[item.ObjectMeta.Name] =
								append(snatPolicyNames[item.ObjectMeta.Name], NAMESPACE)
						}
						// check for namespace match
					}
				} else if res == DEPLOYMENT {
					if len(item.Spec.SnatIp) == 0 {
						matchingServices := agent.getMatchingServices(namespace, label)
						agent.log.Debug("Matching services for deployment ", name, " : ", matchingServices)
						for _, matchingSvc := range matchingServices {
							if util.MatchLabels(item.Spec.Selector.Labels,
								matchingSvc.ObjectMeta.Labels) {
								snatPolicyNames[item.ObjectMeta.Name] =
									append(snatPolicyNames[item.ObjectMeta.Name], SERVICE)
								break
							}
						}
					} else {
						dep := obj.(*appsv1.Deployment)
						if dep.Spec.Selector != nil {
							var matchingPods []*v1.Pod
							depSelector, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
							if err == nil {
								cache.ListAllByNamespace(agent.podInformer.GetIndexer(), namespace, depSelector,
									func(podobj interface{}) {
										matchingPods = append(matchingPods, podobj.(*v1.Pod))
									})
								agent.log.Debug("Matching pods of deployment ", name, " : ", matchingPods)
								for _, po := range matchingPods {
									if util.MatchLabels(item.Spec.Selector.Labels,
										po.ObjectMeta.Labels) {
										snatPolicyNames[item.ObjectMeta.Name] =
											append(snatPolicyNames[item.ObjectMeta.Name], POD)
										break
									}
								}
							} else {
								agent.log.Error(err.Error())
							}
						}

						nsobj, exists, err := agent.nsInformer.GetStore().GetByKey(namespace)
						if err != nil {
							agent.log.Error("Could not lookup snat for " +
								namespace + ": " + err.Error())
							continue
						}
						if !exists || nsobj == nil {
							continue
						}
						if util.MatchLabels(item.Spec.Selector.Labels,
							nsobj.(*v1.Namespace).ObjectMeta.Labels) {
							agent.log.Debug("Deployment namespace : ", nsobj.(*v1.Namespace).ObjectMeta.Name)
							snatPolicyNames[item.ObjectMeta.Name] =
								append(snatPolicyNames[item.ObjectMeta.Name], NAMESPACE)
						}
					}
				}
			}
		}
	}
	return
}

func (agent *HostAgent) isPresentInOpflexSnatLocalInfos(poduids []string, res ResourceType, name string) bool {
	seen := true
	for _, uid := range poduids {
		localinfo, okUId := agent.opflexSnatLocalInfos[uid]
		if !okUId {
			seen = false
			break
		}
		policies, okRes := localinfo.Snatpolicies[res]
		if !okRes {
			seen = false
			break
		}
		found := false
		for _, plcyname := range policies {
			if plcyname == name {
				found = true
				break
			}
		}
		if !found {
			seen = false
			break
		}
	}
	return seen
}

func (agent *HostAgent) handleObjectUpdateForSnat(obj interface{}) {
	agent.snatPolicyCacheMutex.RLock()
	defer agent.snatPolicyCacheMutex.RUnlock()
	if getResourceType(obj) == POD {
		if obj.(*v1.Pod).Status.Phase == v1.PodSucceeded {
			agent.handleObjectDeleteForSnat(obj)
			return
		}
	}
	objKey, err := agent.MetaNamespaceUIDFunc(obj)
	if err != nil {
		agent.log.Error("Could not create snatUpdate object key:" + err.Error())
		return
	}
	plcynames, ok := agent.ReadSnatPolicyLabel(objKey)
	if !ok {
		agent.WriteNewSnatPolicyLabel(objKey)
	}
	sync := false
	if len(plcynames) == 0 {
		polcies := agent.getMatchingSnatPolicy(obj)
		for name, resources := range polcies {
			for _, res := range resources {
				poduids, _ := agent.getPodsMatchingObject(obj, name)
				if len(agent.snatPolicyCache[name].Spec.Selector.Labels) == 0 {
					agent.applyPolicy(poduids, res, name)
				} else {
					agent.applyPolicy(poduids, res, name)
					agent.WriteSnatPolicyLabel(objKey, name, res)
				}
				if len(poduids) > 0 {
					sync = true
				}
			}
		}
	} else {
		var delpodlist []string
		matchnames := agent.getMatchingSnatPolicy(obj)
		agent.log.Info("HandleObject matching policies: ", matchnames)
		seen := make(map[string]bool)
		for name, res := range plcynames {
			poduids, _ := agent.getPodsMatchingObject(obj, name)
			if _, ok := matchnames[name]; !ok {
				for _, uid := range poduids {
					agent.deleteSnatLocalInfo(uid, res, name)
				}
				delpodlist = append(delpodlist, poduids...)
				agent.DeleteSnatPolicyLabelEntry(objKey, name)
				seen[name] = true
			} else if agent.isPresentInOpflexSnatLocalInfos(poduids, res, name) {
				seen[name] = true
			}
		}
		if len(delpodlist) > 0 {
			agent.updateEpFiles(delpodlist)
			sync = true
		}
		for name, resources := range matchnames {
			if seen[name] {
				continue
			}
			for _, res := range resources {
				poduids, _ := agent.getPodsMatchingObject(obj, name)
				agent.applyPolicy(poduids, res, name)
				agent.WriteSnatPolicyLabel(objKey, name, res)
				sync = true
			}
		}
	}
	if sync {
		agent.scheduleSyncNodeInfo()
	}
}

func (agent *HostAgent) handleObjectDeleteForSnat(obj interface{}) {
	objKey, err := agent.MetaNamespaceUIDFunc(obj)
	if err != nil {
		agent.log.Error("Could not create snatDelete object key:" + err.Error())
		return
	}
	agent.snatPolicyCacheMutex.RLock()
	plcynames := agent.getMatchingSnatPolicy(obj)
	agent.snatPolicyCacheMutex.RUnlock()
	var podidlist []string
	sync := false
	for name, resources := range plcynames {
		agent.log.Infof("Handle snatpolicy as object deleted: %s, ObjectKey: %s", name, objKey)
		poduids, _ := agent.getPodsMatchingObject(obj, name)
		for _, uid := range poduids {
			if getResourceType(obj) == SERVICE {
				agent.log.Debug("Service deleted update the localInfo: ", name)
				for _, res := range resources {
					agent.deleteSnatLocalInfo(uid, res, name)
				}
			} else {
				delete(agent.opflexSnatLocalInfos, uid)
				delete(agent.snatPods[name], uid)
			}
		}
		podidlist = append(podidlist, poduids...)
		sync = true
	}
	agent.DeleteSnatPolicyLabel(objKey)
	// Delete any Policy entries present for POD
	if getResourceType(obj) == POD {
		uid := string(obj.(*v1.Pod).ObjectMeta.UID)
		localinfo, ok := agent.opflexSnatLocalInfos[uid]
		if ok {
			for _, policynames := range localinfo.Snatpolicies {
				for _, name := range policynames {
					delete(agent.snatPods[name], uid)
				}
			}
			delete(agent.opflexSnatLocalInfos, uid)
			sync = true
		}
	}
	if sync {
		agent.scheduleSyncNodeInfo()
		if getResourceType(obj) == SERVICE {
			agent.updateEpFiles(podidlist)
		} else {
			agent.scheduleSyncEps()
			agent.scheduleSyncLocalInfo()
		}
	}
}

func (agent *HostAgent) isPolicyNameSpaceMatches(policyName, namespace string) bool {
	policy, ok := agent.snatPolicyCache[policyName]
	if ok {
		if len(policy.Spec.Selector.Namespace) == 0 || (len(policy.Spec.Selector.Namespace) > 0 &&
			policy.Spec.Selector.Namespace == namespace) {
			return true
		}
	}
	return false
}

func (agent *HostAgent) getSnatUuids(poduuid, epfile string) ([]string, error) {
	localInfo := &opflexSnatLocalInfo{}
	plcyUuids := []string{}
	agent.indexMutex.Lock()
	defer agent.indexMutex.Unlock()
	val, check := agent.opflexSnatLocalInfos[poduuid]
	if check {
		if val.Existing {
			agent.log.Debug("Getting existing snat-uuids in ep file : ", epfile)
			agent.log.Debug("snat-uuids in opflexSnatLocalInfos : ", localInfo.PlcyUuids)
			currentEp, err := readEp(epfile)
			if err == nil && currentEp != nil {
				plcyUuids = currentEp.SnatUuid
				agent.log.Debug("snat-uuids in ep file : ", plcyUuids)
				return plcyUuids, nil
			}
		}
		err := util.DeepCopyObj(val, localInfo)
		if err != nil {
			agent.log.Error(err.Error())
			return nil, err
		}
		plcyUuids = localInfo.PlcyUuids
	}
	return plcyUuids, nil
}

func setDestIp(destIp []string) {
	if len(destIp) > 0 {
		sort.Slice(destIp, func(i, j int) bool {
			a := destIp[i]
			b := destIp[j]
			ip_temp := net.ParseIP(a)
			if ip_temp != nil && ip_temp.To4() != nil {
				a += "/32"
			}
			ip_temp = net.ParseIP(b)
			if ip_temp != nil && ip_temp.To4() != nil {
				b += "/32"
			}
			return compareIps(a, b)
		})
	}
}

func compareIps(ipa, ipb string) bool {
	ipB, ipnetB, _ := net.ParseCIDR(ipb)
	_, ipnetA, _ := net.ParseCIDR(ipa)
	if ipnetA.Contains(ipB) {
		// if the Ipa contains the Ipb
		//the above check can be true if example IP CIDR's are 10.10.0.0/16 10.10.0.0/24
		// if ip's are equal check the masks
		if ipnetA.IP.Equal(ipnetB.IP) {
			if bytes.Compare(ipnetA.Mask, ipnetB.Mask) > 0 {
				return true
			}
		}
		return false
	}
	return true
}

func getResourceType(obj interface{}) ResourceType {
	var res ResourceType
	switch obj.(type) {
	case *v1.Pod:
		res = POD
	case *appsv1.Deployment:
		res = DEPLOYMENT
	case *v1.Service:
		res = SERVICE
	case *v1.Namespace:
		res = NAMESPACE
	default:
	}
	return res
}

type ExplicitKey string

func (agent *HostAgent) MetaNamespaceUIDFunc(obj interface{}) (string, error) {
	if key, ok := obj.(ExplicitKey); ok {
		return string(key), nil
	}
	meta, err := meta.Accessor(obj)
	if err != nil {
		return "", err
	}
	if len(meta.GetNamespace()) > 0 {
		return meta.GetNamespace() + "/" + string(meta.GetUID()), nil
	}
	return string(meta.GetUID()), nil
}

func difference(a, b []string) []string {
	mb := make(map[string]struct{}, len(b))
	for _, x := range b {
		mb[x] = struct{}{}
	}
	var diff []string
	for _, x := range a {
		if _, found := mb[x]; !found {
			diff = append(diff, x)
		}
	}
	return diff
}
