// Copyright 2021 BoCloud
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package connector

import (
	"fmt"

	"github.com/fabedge/fabedge/third_party/ipset"
	"k8s.io/apimachinery/pkg/util/sets"
)

const (
	TableFilter             = "filter"
	TableNat                = "nat"
	ChainForward            = "FORWARD"
	ChainPostRouting        = "POSTROUTING"
	ChainFabEdgeForward     = "FABEDGE-FORWARD"
	ChainFabEdgePostRouting = "FABEDGE-POSTROUTING"
	IPSetEdgeNodeIP         = "FABEDGE-EDGE-NODE-IP"
	IPSetCloudPodCIDR       = "FABEDGE-CLOUD-POD-CIDR"
)

func (m *Manager) ensureForwardIPTablesRules(cidr string) error {
	existed, err := m.ipt.ChainExists(TableFilter, ChainFabEdgeForward)
	if err != nil {
		return err
	}

	if !existed {
		return m.ipt.NewChain(TableFilter, ChainFabEdgeForward)
	}

	// ensure rules exist
	if err = m.ipt.AppendUnique(TableFilter, ChainForward, "-j", ChainFabEdgeForward); err != nil {
		return err
	}
	if err = m.ipt.AppendUnique(TableFilter, ChainFabEdgeForward, "-s", cidr, "-j", "ACCEPT"); err != nil {
		return err
	}
	if err = m.ipt.AppendUnique(TableFilter, ChainFabEdgeForward, "-d", cidr, "-j", "ACCEPT"); err != nil {
		return err
	}

	return nil
}

func (m *Manager) ensureSNatIPTablesRules() error {
	existed, err := m.ipt.ChainExists(TableNat, ChainFabEdgePostRouting)
	if err != nil {
		return err
	}

	if !existed {
		return m.ipt.NewChain(TableNat, ChainFabEdgePostRouting)
	}

	if err = m.ipt.AppendUnique(TableNat, ChainPostRouting, "-j", ChainFabEdgePostRouting); err != nil {
		return err
	}

	for _, c := range m.connections {
		for _, addr := range c.LocalAddress {
			exists, err := m.ipt.Exists(TableNat, ChainFabEdgePostRouting, "-m", "set", "--match-set", IPSetEdgeNodeIP, "src", "-m", "set", "--match-set", IPSetCloudPodCIDR, "dst", "-j", "SNAT", "--to", addr)
			if err != nil {
				return err
			}

			if !exists {
				// TODO: fixed connector.IP update issue.
				// now c.LocalAddress contains only one connector.IP,
				// and if there are more than one connector.IP in c.LocalAddress,
				// the processing logic here is going to be problematic
				if err = m.ipt.ClearChain(TableNat, ChainFabEdgePostRouting); err != nil {
					return err
				}

				if err = m.ipt.AppendUnique(TableNat, ChainFabEdgePostRouting, "-m", "set", "--match-set", IPSetEdgeNodeIP, "src", "-m", "set", "--match-set", IPSetCloudPodCIDR, "dst", "-j", "SNAT", "--to", addr); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func (m *Manager) syncEdgeNodeIPSet() error {
	ipsetObj, err := m.ensureIPSet(IPSetEdgeNodeIP, ipset.HashIP)
	if err != nil {
		return err
	}

	allEdgeNodeIPs := m.getAllEdgeNodeIPs()

	oldEdgeNodeIPs, err := m.getOldEdgeNodeIPs()

	if err != nil {
		return err
	}

	needAddEdgeNodeIPs := allEdgeNodeIPs.Difference(oldEdgeNodeIPs)
	for ip := range needAddEdgeNodeIPs {
		if err := m.addIPSetEntry(ipsetObj, ip, ipset.HashIP); err != nil {
			return err
		}
	}

	needDelEdgeNodeIPs := oldEdgeNodeIPs.Difference(allEdgeNodeIPs)
	for ip := range needDelEdgeNodeIPs {
		if err := m.delIPSetEntry(ipsetObj, ip, ipset.HashIP); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) ensureIPSet(setName string, setType ipset.Type) (*ipset.IPSet, error) {
	set := &ipset.IPSet{
		Name:    setName,
		SetType: setType,
	}
	if err := m.ipset.CreateSet(set, true); err != nil {
		return nil, err
	}
	return set, nil
}

func (m *Manager) getAllEdgeNodeIPs() sets.String {
	ips := sets.NewString()
	for _, c := range m.connections {
		ips.Insert(c.RemoteAddress...)
	}
	return ips
}

func (m *Manager) getOldEdgeNodeIPs() (sets.String, error) {
	ips := sets.NewString()
	entries, err := m.ipset.ListEntries(IPSetEdgeNodeIP)
	if err != nil {
		return nil, err
	}
	ips.Insert(entries...)
	return ips, nil
}

func (m *Manager) addIPSetEntry(set *ipset.IPSet, ip string, setType ipset.Type) error {
	entry := &ipset.Entry{
		SetType: setType,
	}

	switch setType {
	case ipset.HashIP:
		entry.IP = ip
	case ipset.HashNet:
		entry.Net = ip
	}

	if !entry.Validate(set) {
		return fmt.Errorf("failed to validate ipset entry, ipset: %v, entry: %v", set, entry)
	}

	return m.ipset.AddEntry(entry.String(), set, true)
}

func (m *Manager) delIPSetEntry(set *ipset.IPSet, ip string, setType ipset.Type) error {
	entry := &ipset.Entry{
		SetType: setType,
	}

	switch setType {
	case ipset.HashIP:
		entry.IP = ip
	case ipset.HashNet:
		entry.Net = ip
	}

	if !entry.Validate(set) {
		return fmt.Errorf("failed to validate ipset entry, ipset: %v, entry: %v", set, entry)
	}

	return m.ipset.DelEntry(entry.String(), set.Name)
}

func (m *Manager) syncCloudPodCIDRSet() error {
	ipsetObj, err := m.ensureIPSet(IPSetCloudPodCIDR, ipset.HashNet)
	if err != nil {
		return err
	}

	allCloudPodCIDRs := m.getAllCloudPodCIDRs()

	oldCloudPodCIDRs, err := m.getOldCloudPodCIDRS()

	if err != nil {
		return err
	}

	needAddCloudPodCIDRs := allCloudPodCIDRs.Difference(oldCloudPodCIDRs)
	for cidr := range needAddCloudPodCIDRs {
		if err := m.addIPSetEntry(ipsetObj, cidr, ipset.HashNet); err != nil {
			return err
		}
	}

	needDelCloudPodCIDRs := oldCloudPodCIDRs.Difference(allCloudPodCIDRs)
	for cidr := range needDelCloudPodCIDRs {
		if err := m.delIPSetEntry(ipsetObj, cidr, ipset.HashNet); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) getAllCloudPodCIDRs() sets.String {
	cidrs := sets.NewString()
	for _, c := range m.connections {
		cidrs.Insert(c.LocalSubnets...)
	}
	return cidrs
}

func (m *Manager) getOldCloudPodCIDRS() (sets.String, error) {
	cidrs := sets.NewString()
	entries, err := m.ipset.ListEntries(IPSetCloudPodCIDR)
	if err != nil {
		return nil, err
	}
	cidrs.Insert(entries...)
	return cidrs, nil
}

func (m *Manager) SNatIPTablesRulesCleanup() error {
	return m.ipt.ClearChain(TableNat, ChainFabEdgePostRouting)
}
