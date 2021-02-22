// Copyright 2021 Cisco Systems, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controller

import (
	erspanpolicy "github.com/noironetworks/aci-containers/pkg/erspanpolicy/apis/aci.erspan/v1alpha"
	podIfpolicy "github.com/noironetworks/aci-containers/pkg/gbpcrd/apis/acipolicy/v1"
	"github.com/noironetworks/aci-containers/pkg/ipam"
	tu "github.com/noironetworks/aci-containers/pkg/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"net"
	"testing"
	"time"
)

type erspanTest struct {
	erspanPol   *erspanpolicy.ErspanPolicy
	writeToApic bool
	desc        string
	spanDel     bool
}

type podifdata struct {
	MacAddr    string
	EPG        string
	Namespace  string
	AppProfile string
}

type spanpolicy struct {
	name      string
	namespace string
	dest      erspanpolicy.ErspanDestType
	source    erspanpolicy.ErspanSourceType
	labels    map[string]string
}

func staticErspanKey() string {
	return "kube_nfp_static"
}

func spanpolicydata(name string, namespace string, dest erspanpolicy.ErspanDestType,
	source erspanpolicy.ErspanSourceType, labels map[string]string) *erspanpolicy.ErspanPolicy {
	policy := &erspanpolicy.ErspanPolicy{
		Spec: erspanpolicy.ErspanPolicySpec{
			Dest:   dest,
			Source: source,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: erspanpolicy.ErspanPolicyStatus{
			State: erspanpolicy.Ready,
		},
	}
	var podSelector erspanpolicy.PodSelector
	podSelector.Namespace = namespace
	podSelector.Labels = labels
	policy.Spec.Selector = podSelector
	return policy
}

var erspanDestType1 = erspanpolicy.ErspanDestType{
	DestIP: "1.1.1.1",
	FlowID: 2,
}

var erspanDestType2 = erspanpolicy.ErspanDestType{
	DestIP: "1.1.1.1",
}

var erspanSourceType1 = erspanpolicy.ErspanSourceType{
	AdminState: "start",
	Direction:  "both",
}

var erspanSourceType2 = erspanpolicy.ErspanSourceType{
	AdminState: "",
	Direction:  "",
}

var spanTests = []spanpolicy{
	{
		"span_policy1",
		"testns",
		erspanDestType1,
		erspanSourceType1,
		map[string]string{"key": "value"},
	},
	{
		"span_policy2",
		"testns",
		erspanDestType2,
		erspanSourceType2,
		map[string]string{"key": "value"},
	},
}

func podifinfodata(name string, namespace string, macaddr string,
	epg string) *podIfpolicy.PodIF {
	podifinfo := &podIfpolicy.PodIF{
		Status: podIfpolicy.PodIFStatus{
			MacAddr: macaddr,
			EPG:     epg,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	return podifinfo
}

func checkDeleteErspan(t *testing.T, spanTest erspanTest, cont *testAciController) {
	tu.WaitFor(t, "delete", 500*time.Millisecond,
		func(last bool) (bool, error) {
			//span := spanTests[0]
			key := cont.aciNameForKey("span",
				spanTest.erspanPol.Namespace+"_"+spanTest.erspanPol.Name)
			if !tu.WaitEqual(t, last, 0,
				len(cont.apicConn.GetDesiredState(key)), "delete") {
				return false, nil
			}
			return true, nil
		})
}

var podifTests = []podifdata{
	{
		"C2-85-53-A1-85-61",
		"test-epg1",
		"testns",
		"test-ap1",
	},
	{
		"C2-85-53-A1-85-62",
		"test-epg2",
		"testns",
		"test-ap2",
	},
	{
		"C2-85-53-A1-85-63",
		"test-epg3",
		"testns",
		"test-ap3",
	},
}

func podifwait(t *testing.T, desc string, expected map[string]podifdata,
	actual map[string]*EndPointData) {
	tu.WaitFor(t, desc, 100*time.Millisecond, func(last bool) (bool, error) {
		for key, v := range expected {
			val, ok := actual[key]
			if ok {
				result := tu.WaitEqual(t, last, v.MacAddr, val.MacAddr, "MacAddr does not match") &&
					tu.WaitEqual(t, last, v.EPG[0], val.EPG[0], "EPG does not match")
				return result, nil
			} else {
				return false, nil
			}
		}
		return true, nil
	})

}

func TestErspanPolicy(t *testing.T) {
	cont := testController()
	initCont := func() *testAciController {
		cont := testController()
		cont.config.NodeServiceIpPool = []ipam.IpRange{
			{Start: net.ParseIP("10.1.1.2"), End: net.ParseIP("10.1.1.3")},
		}
		cont.config.PodIpPool = []ipam.IpRange{
			{Start: net.ParseIP("10.1.1.2"), End: net.ParseIP("10.1.255.254")},
		}
		cont.AciController.initIpam()

		cont.fakeNamespaceSource.Add(namespaceLabel("testns",
			map[string]string{"test": "testv"}))
		cont.fakeNamespaceSource.Add(namespaceLabel("ns1",
			map[string]string{"nl": "nv"}))
		cont.fakeNamespaceSource.Add(namespaceLabel("ns2",
			map[string]string{"nl": "nv"}))

		return cont
	}
	//Function to check if erspan object is present in the apic connection at a specific key
	// erspanObject := func(t *testing.T, desc string, cont *testAciController,
	// key string, expected string, present bool) {

	// tu.WaitFor(t, desc, 500*time.Millisecond,
	// func(last bool) (bool, error) {
	// cont.indexMutex.Lock()
	// defer cont.indexMutex.Unlock()
	// var ok bool
	// ds := cont.apicConn.GetDesiredState(key)
	// for _, v := range ds {
	// if _, ok = v[expected]; ok {
	// break
	// }
	// }
	// if ok == present {
	// return true, nil
	// }
	// return false, nil
	// })
	// cont.log.Info("Finished waiting for ", desc)

	// }
	cont.run()
	for _, pt := range podifTests {
		cont := initCont()
		cont.log.Info("Testing podif data")
		podifObj := podifinfodata(pt.MacAddr, pt.EPG, pt.Namespace, pt.AppProfile)
		cont.fakePodIFSource.Add(podifObj)
		cont.log.Debug("podIF Added: ", podifObj)

	}
	cont.stop()

	for _, st := range spanTests {
		cont := initCont()
		cont.run()
		cont.log.Info("Testing erspan create for ", st.name)
		spanObj := spanpolicydata(st.name, st.namespace, st.dest, st.source, st.labels)
		cont.log.Info("Testing erspan obj", spanObj)
		cont.fakeErspanPolicySource.Add(spanObj)
		cont.log.Debug("Erspan Added: ", spanObj)
		//erspanObject(t, "object absent check", cont, st.name, "spanVSrcGrp", false)
		cont.stop()
	}

	for _, st := range spanTests {
		cont := initCont()
		cont.run()
		cont.log.Info("Testing erspan delete")
		spanObj := spanpolicydata(st.name, st.namespace, st.dest, st.source, st.labels)
		cont.fakeErspanPolicySource.Delete(spanObj)
		cont.stop()
	}

}
