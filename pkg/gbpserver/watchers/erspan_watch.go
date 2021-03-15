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
	erspanSessionSub = "SpanSession"
	erspanSrcGrpSub  = "SpanSrcGrp"
	erspanSrcSub     = "SpanSrcMember"
	erspanRefRSrcSub = "SpanMemberToRefRSrc"
	erspanDstGrpSub  = "SpanDstGrp"
	erspanDstSub     = "SpanDstMember"
	erspanDstSumSub  = "SpanDstSummary"
)

type erspanSession struct {
	Name       string `json:"name"`
	AdminState string `json:"adminState,omitempty"`
}

type erspanSrcGrp struct {
	Name string `json:"name"`
}

type erspanSrcMember struct {
	Name      string `json:"name"`
	Direction string `json:"direction,omitempty"`
}

type erspanMemberToRefRSrc struct {
	Name   string `json:"name"`
	Target string
}

type erspanDstGrp struct {
	Name string `json:"name"`
}

type erspanDstMember struct {
	Name string `json:"name"`
}

type erspanDstSummary struct {
	Name   string `json:"name"`
	DestIP string `json:"destIP"`
	FlowID int    `json:"flowID,omitempty"`
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
	erspanSessionMO := &erspanSession{
		Name:       span.ObjectMeta.Name,
		AdminState: span.Spec.Source.AdminState,
		// DestIP:     span.Spec.Dest.DestIP,
		// FlowID:     span.Spec.Dest.FlowID,
	}
	spw.gs.AddGBPCustomMo(erspanSessionMO)
	srcGrpMO := &erspanSrcGrp{
		Name: span.ObjectMeta.Name,
	}
	spw.gs.AddGBPCustomMo(srcGrpMO)
	srcMemberMO := &erspanSrcMember{
		Name:      span.ObjectMeta.Name,
		Direction: span.Spec.Source.Direction,
	}
	spw.gs.AddGBPCustomMo(srcMemberMO)
}

func (spw *ErspanWatcher) erspanDeleted(obj interface{}) {
	span, ok := obj.(*erspanpolicy.ErspanPolicy)
	if !ok {
		spw.log.Errorf("erspanDeleted: Bad object type")
		return
	}
	spw.log.Infof("erspanDeleted - %s", span.ObjectMeta.Name)
	sessionMO := &erspanSession{
		Name:       span.ObjectMeta.Name,
		AdminState: span.Spec.Source.AdminState,
	}
	spw.gs.DelGBPCustomMo(sessionMO)
	srcGrpMO := &erspanSrcGrp{
		Name: span.ObjectMeta.Name,
	}
	spw.gs.DelGBPCustomMo(srcGrpMO)
	srcMemberMO := &erspanSrcMember{
		Name:      span.ObjectMeta.Name,
		Direction: span.Spec.Source.Direction,
	}
	spw.gs.DelGBPCustomMo(srcMemberMO)
}

func (session *erspanSession) Subject() string {
	return erspanSessionSub
}

func (session *erspanSession) URI(gs *gbpserver.Server) string {
	spanParentURI := gs.GetURIBySubject(spanUniSub)
	return fmt.Sprintf("%s%s/%s/", spanParentURI, erspanSessionSub, session.Name)
}

func (session *erspanSession) Properties() map[string]interface{} {

	return map[string]interface{}{
		"name":  session.Name,
		"state": session.AdminState,
		// "dest":   sp.DestIP,
		// "flowId": sp.FlowID,
	}
}

func (session *erspanSession) ParentSub() string {
	return spanUniSub
}

func (session *erspanSession) ParentURI(gs *gbpserver.Server) string {
	spanParentURI := gs.GetURIBySubject(spanUniSub)
	return spanParentURI
}

func (session *erspanSession) Children() []string {
	return []string{fmt.Sprintf("/%s/%s/%s/%s/%s/", spanUniSub, erspanSessionSub, session.Name, erspanSrcGrpSub, session.Name),
		fmt.Sprintf("/%s/%s/%s/%s/%s/", spanUniSub, erspanSessionSub, session.Name, erspanDstGrpSub, session.Name)}
}

func (srcGrp *erspanSrcGrp) Subject() string {
	return erspanSrcGrpSub
}

func (srcGrp *erspanSrcGrp) URI(gs *gbpserver.Server) string {
	spanParentURI := gs.GetURIBySubject(spanUniSub)
	return fmt.Sprintf("%s%s/%s/%s/%s/", spanParentURI, erspanSessionSub, srcGrp.Name,
		erspanSrcGrpSub, srcGrp.Name)
}

func (srcGrp *erspanSrcGrp) Properties() map[string]interface{} {

	return map[string]interface{}{
		"name": srcGrp.Name,
	}
}

func (srcGrp *erspanSrcGrp) ParentSub() string {
	return erspanSessionSub
}

func (srcGrp *erspanSrcGrp) ParentURI(gs *gbpserver.Server) string {
	spanParentURI := gs.GetURIBySubject(spanUniSub)
	return fmt.Sprintf("%s%s/%s/", spanParentURI, erspanSessionSub, srcGrp.Name)
}

func (srcGrp *erspanSrcGrp) Children() []string {
	return []string{fmt.Sprintf("/%s/%s/%s/%s/%s/%s/%s/", spanUniSub, erspanSessionSub,
		srcGrp.Name, erspanSrcGrpSub, srcGrp.Name, erspanSrcSub, srcGrp.Name)}
}

func (SrcMember *erspanSrcMember) Subject() string {
	return erspanSrcSub
}

func (SrcMember *erspanSrcMember) URI(gs *gbpserver.Server) string {
	spanParentURI := gs.GetURIBySubject(spanUniSub)
	return fmt.Sprintf("%s%s/%s/%s/%s/%s/%s/", spanParentURI, erspanSessionSub, SrcMember.Name,
		erspanSrcGrpSub, SrcMember.Name, erspanSrcSub, SrcMember.Name)
}

func (SrcMember *erspanSrcMember) Properties() map[string]interface{} {

	return map[string]interface{}{
		"name": SrcMember.Name,
		"dir":  SrcMember.Direction,
	}
}

func (SrcMember *erspanSrcMember) ParentSub() string {
	return erspanSrcGrpSub
}

func (SrcMember *erspanSrcMember) ParentURI(gs *gbpserver.Server) string {
	spanParentURI := gs.GetURIBySubject(spanUniSub)
	return fmt.Sprintf("%s%s/%s/%s/%s/", spanParentURI, erspanSessionSub, SrcMember.Name,
		erspanSrcGrpSub, SrcMember.Name)
}

func (SrcMember *erspanSrcMember) Children() []string {
	return []string{fmt.Sprintf("/%s/%s/%s/%s/%s/%s/%s/%s/", spanUniSub, erspanSessionSub,
		SrcMember.Name, erspanSrcGrpSub, SrcMember.Name, erspanSrcSub, SrcMember.Name, erspanRefRSrcSub)}
}

func (SrcRef *erspanMemberToRefRSrc) Subject() string {
	return erspanSrcSub
}

func (SrcRef *erspanMemberToRefRSrc) URI(gs *gbpserver.Server) string {
	spanParentURI := gs.GetURIBySubject(spanUniSub)
	return fmt.Sprintf("%s%s/%s/%s/%s/%s/%s/%s/", spanParentURI, erspanSessionSub, SrcRef.Name,
		erspanSrcGrpSub, SrcRef.Name, erspanSrcSub, SrcRef.Name, erspanRefRSrcSub)
}

func (SrcRef *erspanMemberToRefRSrc) Properties() map[string]interface{} {

	return map[string]interface{}{}
}

func (SrcRef *erspanMemberToRefRSrc) ParentSub() string {
	return erspanSrcGrpSub
}

func (SrcRef *erspanMemberToRefRSrc) ParentURI(gs *gbpserver.Server) string {
	spanParentURI := gs.GetURIBySubject(spanUniSub)
	return fmt.Sprintf("%s%s/%s/%s/%s/%s/%s/", spanParentURI, erspanSessionSub, SrcRef.Name,
		erspanSrcGrpSub, SrcRef.Name, erspanSrcSub, SrcRef.Name)
}

func (SrcRef *erspanMemberToRefRSrc) Children() []string {
	return []string{}
}
