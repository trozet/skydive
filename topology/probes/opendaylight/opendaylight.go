/*
 * Copyright (C) 2018 Red Hat, Inc.
 *
 * Licensed to the Apache Software Foundation (ASF) under one
 * or more contributor license agreements.  See the NOTICE file
 * distributed with this work for additional information
 * regarding copyright ownership.  The ASF licenses this file
 * to you under the Apache License, Version 2.0 (the
 * "License"); you may not use this file except in compliance
 * with the License.  You may obtain a copy of the License at
 *
 *  http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations
 * under the License.
 *
 */

package opendaylight

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/skydive-project/skydive/topology"

	"github.com/skydive-project/skydive/common"
	"github.com/skydive-project/skydive/config"
	"github.com/skydive-project/skydive/filters"
	"github.com/skydive-project/skydive/logging"
	"github.com/skydive-project/skydive/topology/graph"
)

type odlNodeInterface struct {
	Iface string `json:"tp-id"`
}

type odlNode struct {
	NodeID string             `json:"node-id"`
	Ifaces []odlNodeInterface `json:"termination-point"`
}

type odlLinkSource struct {
	SourceTP   string `json:"source-tp"`
	SourceNode string `json:"source-node"`
}

type odlDestSource struct {
	DestTP   string `json:"dest-tp"`
	DestNode string `json:"dest-node"`
}

type odlLink struct {
	LinkID string        `json:"link-id"`
	Source odlLinkSource `json:"source"`
	Dest   odlDestSource `json:"destination"`
}

type odlTopo struct {
	Topology []struct {
		TopologyID string    `json:"topology-id"`
		OdlNodes   []odlNode `json:"node"`
		OdlLinks   []odlLink `json:"link"`
	} `json:"topology"`
}

type Probe struct {
	graph.DefaultGraphListener
	graph      *graph.Graph
	httpClient http.Client
	state      int64
	odlPort    int
	odlHost    string
	odlUser    string
	odlPass    string
}

func updateTopology(data []byte, p *Probe) {
	var topo = new(odlTopo)
	var switchNode *graph.Node
	var ifaceNode *graph.Node
	err := json.Unmarshal(data, &topo)
	if err != nil {
		fmt.Println("Error unmarshaling ODL output", err)
		return
	}
	fmt.Println("topology id is", topo.Topology[0].TopologyID)
	fmt.Println("topology nodes are", topo.Topology[0].OdlNodes)
	for _, node := range topo.Topology[0].OdlNodes {
		switchNode = p.graph.NewNode(graph.GenID(node.NodeID), graph.Metadata{"Name": node.NodeID, "Type": "switch"})
		for _, iface := range node.Ifaces {
			ifaceNode = p.graph.NewNode(graph.GenID(node.NodeID, iface.Iface), graph.Metadata{"Name": iface.Iface, "Type": "device"})
			topology.AddOwnershipLink(p.graph, switchNode, ifaceNode, nil)
		}
	}

	for _, link := range topo.Topology[0].OdlLinks {
		p.graph.NewEdge(graph.GenID(link.LinkID), p.graph.GetNode(graph.GenID(link.Source.SourceNode, link.Source.SourceTP)),
			p.graph.GetNode(graph.GenID(link.Dest.DestNode, link.Dest.DestTP)), graph.Metadata{"Name": link.LinkID, "Type": "layer2"})
	}

	// TODO clean up nodes, links for delete
	filter := graph.NewElementFilter(filters.NewTermStringFilter("Type", "switch"))
	var nodeFound bool
	for _, gNode := range p.graph.GetNodes(filter) {
		nodeFound = false
		for _, node := range topo.Topology[0].OdlNodes {
			if graph.GenID(node.NodeID) == gNode.ID {
				nodeFound = true
				break
			}
		}
		if !nodeFound {
			p.graph.DelNode(gNode)
		}
	}

	linkFilter := graph.NewElementFilter(filters.NewTermStringFilter("Type", "layer2"))
	var linkFound bool
	for _, gLink := range p.graph.GetEdges(linkFilter) {
		linkFound = false
		for _, link := range topo.Topology[0].OdlLinks {
			if graph.GenID(link.LinkID) == gLink.ID {
				linkFound = true
				break
			}
		}
		if !linkFound {
			p.graph.DelEdge(gLink)
		}
	}
}

// this is just a hack for now to test out visualizing a VPN link
// perhaps could add metadata for VPN on each site and connect it that way
func updateVpnLink(p *Probe) {
	ceSiteA := p.graph.GetNode(graph.GenID("CE-SiteA"))
	pe1 := p.graph.GetNode(graph.GenID("cisco-ios1"))
	linkTrigger := p.graph.GetEdge(graph.GenID("CE-SiteA"))
	vpnA := p.graph.NewNode(graph.GenID("VPN-Blue"), graph.Metadata{"Name": "VPN-Blue", "Type": "vpn"})
	topology.AddOwnershipLink(p.graph, vpnA, ceSiteA, nil)

	vpnB := p.graph.NewNode(graph.GenID("VPN-Blue2"), graph.Metadata{"Name": "VPN-Blue2", "Type": "vpn"})
	ceSiteB := p.graph.GetNode(graph.GenID("CE-SiteB"))
	topology.AddOwnershipLink(p.graph, vpnB, ceSiteB, nil)

	mplsCloud := p.graph.NewNode(graph.GenID("MPLS-Backbone"), graph.Metadata{"Name": "MPLS-Backbone", "Type": "vpn"})
	pe2 := p.graph.GetNode(graph.GenID("cisco-ios2"))
	topology.AddOwnershipLink(p.graph, mplsCloud, pe1, nil)
	topology.AddOwnershipLink(p.graph, mplsCloud, pe2, nil)

	if ceSiteA == nil || pe1 == nil {
		fmt.Println("No VPN sites detected")
		deleteVpnLink(p)
		return
	} else if linkTrigger == nil {
		fmt.Println("No VPN detected, no connectivity between ceSiteA and pe1")
		deleteVpnLink(p)
		return
	} else {
		p.graph.NewEdge(graph.GenID("VPN link A"), vpnA, mplsCloud, graph.Metadata{"Name": "Blue VPN 1", "RelationType": "service"})
		p.graph.NewEdge(graph.GenID("VPN link B"), vpnB, mplsCloud, graph.Metadata{"Name": "Blue VPN 2", "RelationType": "service"})
	}
}

func deleteVpnLink(p *Probe) {
	linkFilter := graph.NewElementFilter(filters.NewTermStringFilter("RelationType", "service"))
	for _, gLink := range p.graph.GetEdges(linkFilter) {
		p.graph.DelEdge(gLink)
	}
}

func (p *Probe) run() {
	atomic.StoreInt64(&p.state, common.RunningState)
	fmt.Println("Starting OpenDaylight Probe...")
	logging.GetLogger().Info("Starting OpenDaylight Probe")
	odlURL := fmt.Sprintf("http://%s:%d/restconf/operational/network-topology:network-topology/topology/flow:1", p.odlHost, p.odlPort)
	req, _ := http.NewRequest("GET", odlURL, nil)
	req.SetBasicAuth(p.odlUser, p.odlPass)
	for atomic.LoadInt64(&p.state) == common.RunningState {
		p.graph.Lock()
		response, err := p.httpClient.Do(req)
		if err != nil {
			fmt.Printf("The HTTP request failed with error %s\n", err)
		} else {
			data, err := ioutil.ReadAll(response.Body)
			if err != nil {
				panic(err.Error)
			}
			fmt.Println(string(data))
			updateTopology(data, p)
			updateVpnLink(p)
		}
		p.graph.Unlock()
		time.Sleep(1 * time.Second)
	}
}

func (p *Probe) Start() {
	go p.run()
}

func (p *Probe) Stop() {
	atomic.CompareAndSwapInt64(&p.state, common.RunningState, common.StoppingState)
}

func NewProbe(g *graph.Graph) (*Probe, error) {

	probe := &Probe{
		graph:   g,
		odlHost: config.GetString("opendaylight.host"),
		odlPass: config.GetString("opendaylight.password"),
		odlUser: config.GetString("opendaylight.user"),
		odlPort: config.GetInt("opendaylight.port"),
	}
	atomic.StoreInt64(&probe.state, common.StoppedState)

	return probe, nil
}
