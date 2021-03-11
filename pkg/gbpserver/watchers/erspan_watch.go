/***
Copyright 2021 Cisco Systems Inc. All rights reserved.

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

package watchers

import (
	"fmt"
	erspanpolicy "github.com/noironetworks/aci-containers/pkg/erspanpolicy/apis/aci.erspan/v1alpha"
	erspanclientset "github.com/noironetworks/aci-containers/pkg/erspanpolicy/clientset/versioned"
	"github.com/noironetworks/aci-containers/pkg/gbpserver"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

const (
	spanUniSub       = "SpanUniverse"
	erspanParentSub  = "SpanSession"
	erspanSrcGrpSub  = "SpanSrcGrp"
	erspanSrcSub     = "SpanSrcMember"
	erspanRefRSrcSub = "SpanMemberToRefRSrc"
	erspanDstGrpSub  = "SpanDstGrp"
	erspanDstSub     = "SpanDstMember"
	erspanDstSumSub  = "SpanDstSummary"
)

type erspanCRD struct {
	Name       string `json:"name"`
	AdminState string `json:"adminState,omitempty"`
	Direction  string `json:"direction,omitempty"`
	DestIP     string `json:"destIP"`
	FlowID     int    `json:"flowID,omitempty"`
}

type ErspanWatcher struct {
	log *log.Entry
	gs  *gbpserver.Server
	rc  restclient.Interface
}

func NewErspanWatcher(gs *gbpserver.Server) (*ErspanWatcher, error) {
	gcfg := gs.Config()
	level, err := log.ParseLevel(gcfg.WatchLogLevel)
	if err != nil {
		panic(err.Error())
	}
	logger := log.New()
	logger.Level = level
	log := logger.WithField("mod", "ERSPAN-W")
	cfg, err := restclient.InClusterConfig()
	if err != nil {
		return nil, err
	}

	erspanclient, err := erspanclientset.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	restClient := erspanclient.AciV1alpha().RESTClient()
	return &ErspanWatcher{
		log: log,
		rc:  restClient,
		gs:  gs,
	}, nil
}

func (spw *ErspanWatcher) InitErspanInformer(stopCh <-chan struct{}) {
	spw.watchErspan(stopCh)
}

func (spw *ErspanWatcher) watchErspan(stopCh <-chan struct{}) {

	ErspanLw := cache.NewListWatchFromClient(spw.rc, "erspanpolicies", metav1.NamespaceAll, fields.Everything())
	_, erspanInformer := cache.NewInformer(ErspanLw, &erspanpolicy.ErspanPolicy{}, 0,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				spw.erspanAdded(obj)
			},
			UpdateFunc: func(oldobj interface{}, newobj interface{}) {
				spw.erspanAdded(newobj)
			},
			DeleteFunc: func(obj interface{}) {
				spw.erspanDeleted(obj)
			},
		})
	go erspanInformer.Run(stopCh)
}

func (spw *ErspanWatcher) erspanAdded(obj interface{}) {
	span, ok := obj.(*erspanpolicy.ErspanPolicy)
	if !ok {
		spw.log.Errorf("erspanAdded: Bad object type")
		return
	}
	spw.log.Infof("erspanAdded - %s", span.ObjectMeta.Name)
	erspanMO := &erspanCRD{
		Name:       span.ObjectMeta.Name,
		AdminState: span.Spec.Source.AdminState,
		Direction:  span.Spec.Source.Direction,
		DestIP:     span.Spec.Dest.DestIP,
		FlowID:     span.Spec.Dest.FlowID,
	}
	spw.gs.AddGBPCustomMo(erspanMO)
}

func (spw *ErspanWatcher) erspanDeleted(obj interface{}) {
	span, ok := obj.(*erspanpolicy.ErspanPolicy)
	if !ok {
		spw.log.Errorf("erspanDeleted: Bad object type")
		return
	}
	spw.log.Infof("erspanDeleted - %s", span.ObjectMeta.Name)
	erspanMO := &erspanCRD{
		Name:       span.ObjectMeta.Name,
		AdminState: span.Spec.Source.AdminState,
		Direction:  span.Spec.Source.Direction,
		DestIP:     span.Spec.Dest.DestIP,
		FlowID:     span.Spec.Dest.FlowID,
	}
	spw.gs.DelGBPCustomMo(erspanMO)
}

func (sp *erspanCRD) Subject() string {
	return erspanSrcGrpSub
}

func (sp *erspanCRD) URI(gs *gbpserver.Server) string {
	spanParentURI := gs.GetURIBySubject(spanUniSub)
	return fmt.Sprintf("%s%s/", spanParentURI, sp.Name)
}

func (sp *erspanCRD) Properties() map[string]interface{} {

	return map[string]interface{}{
		"name":   sp.Name,
		"state":  sp.AdminState,
		"dir":    sp.Direction,
		"dest":   sp.DestIP,
		"flowId": sp.FlowID,
	}
}

func (sp *erspanCRD) ParentSub() string {
	return erspanParentSub
}

func (sp *erspanCRD) ParentURI(gs *gbpserver.Server) string {
	spanParentURI := gs.GetURIBySubject(spanUniSub)
	return spanParentURI
}

func (sp *erspanCRD) Children() []string {
	return []string{}
}
