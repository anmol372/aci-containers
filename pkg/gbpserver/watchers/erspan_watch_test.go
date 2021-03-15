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
	"github.com/davecgh/go-spew/spew"
	//erspanpolicy "github.com/noironetworks/aci-containers/pkg/erspanpolicy/apis/aci.erspan/v1alpha"
	"github.com/noironetworks/aci-containers/pkg/gbpserver"
	log "github.com/sirupsen/logrus"
	// "github.com/stretchr/testify/assert"
	"reflect"
	//"testing"
	"time"
)

//var err error

type erspanSuite struct {
	s   *gbpserver.Server
	spw *ErspanWatcher
	//spgbp *erspanCRD
}

func (sp *erspanSuite) setup() {

	gCfg := &gbpserver.GBPServerConfig{}
	gCfg.WatchLogLevel = "info"
	log := log.WithField("mod", "test")
	sp.s = gbpserver.NewServer(gCfg)
	sp.spw, err = NewErspanWatcher(sp.s)

	sp.spw = &ErspanWatcher{
		log: log,
		gs:  sp.s,
	}
}

func (sp *erspanSuite) expectMsg(op int, msg interface{}) error {
	gotOp, gotMsg, err := sp.s.UTReadMsg(200 * time.Millisecond)
	if err != nil {
		return err
	}

	if gotOp != op {
		return fmt.Errorf("Exp op: %d, got: %d", op, gotOp)
	}

	if !reflect.DeepEqual(msg, gotMsg) {
		spew.Dump(msg)
		spew.Dump(gotMsg)
		return fmt.Errorf("msgs don't match")
	}

	return nil
}
