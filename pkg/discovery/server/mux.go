// Copyright 2018 deepfabric, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"

	"github.com/deepfabric/elasticell-operator/pkg/client/clientset/versioned"
	"github.com/deepfabric/elasticell-operator/pkg/discovery"
	restful "github.com/emicklei/go-restful"
	"github.com/golang/glog"
)

type server struct {
	discovery discovery.CellDiscovery
}

// StartServer starts a Cell Discovery server
func StartServer(cli versioned.Interface, port int) {
	svr := &server{discovery.NewCellDiscovery(cli)}

	ws := new(restful.WebService)
	ws.Route(ws.GET("/new/{advertise-peer-url}").To(svr.newPdHandler))
	ws.Route(ws.GET("/proxy-config").To(svr.newProxyHandler))
	restful.Add(ws)

	glog.Infof("starting Cell Discovery server, listening on 0.0.0.0:%d", port)
	glog.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}

func (svr *server) newPdHandler(req *restful.Request, resp *restful.Response) {
	encodedAdvertisePeerUrl := req.PathParameter("advertise-peer-url")
	data, err := base64.StdEncoding.DecodeString(encodedAdvertisePeerUrl)
	if err != nil {
		glog.Errorf("failed to decode advertise-peer-url: %s", encodedAdvertisePeerUrl)
		if err := resp.WriteError(http.StatusInternalServerError, err); err != nil {
			glog.Errorf("failed to writeError: %v", err)
		}
		return
	}
	advertisePeerUrl := string(data)

	result, err := svr.discovery.Discover(advertisePeerUrl)
	if err != nil {
		glog.Errorf("failed to discover: %s, %v", advertisePeerUrl, err)
		if err := resp.WriteError(http.StatusInternalServerError, err); err != nil {
			glog.Errorf("failed to writeError: %v", err)
		}
		return
	}

	glog.Infof("generated pd args for %s: %s", advertisePeerUrl, result)
	if _, err := io.WriteString(resp, result); err != nil {
		glog.Errorf("failed to writeString: %s, %v", result, err)
	}
}

func (svr *server) newProxyHandler(req *restful.Request, resp *restful.Response) {
	result, err := svr.discovery.GetProxyConfig()
	if err != nil {
		glog.Errorf("failed to get proxy config: %v", err)
		if err := resp.WriteError(http.StatusInternalServerError, err); err != nil {
			glog.Errorf("failed to writeError: %v", err)
		}
		return
	}
	glog.Infof("generated proxy config json: %s", result)
	if _, err := io.WriteString(resp, result); err != nil {
		glog.Errorf("failed to writeString: %s, %v", result, err)
	}
}