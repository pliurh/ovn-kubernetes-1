package iptables

import (
	"fmt"
	"strings"

	"github.com/coreos/go-iptables/iptables"

	"k8s.io/klog/v2"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	utilerrors "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util/errors"
)

// Rule represents an iptables rule.
type Rule struct {
	Table    string
	Chain    string
	Args     []string
	Protocol iptables.Protocol
}

// RestoreRulesFiltered adds the given rules to iptables.
// filter is a map[table][chain] of valid tables/chains to use for filtering rules to be added.
// If no rule exists for the filter, the chain will still be restored as empty.
func RestoreRulesFiltered(rules []Rule, filter map[string]map[string]struct{}) error {
	var err error
	var errs []error
	var ipt util.IPTablesHelper

	// stores the rules we want to program, keyed by protocol, table, chain
	ruleMap := map[iptables.Protocol]map[string]map[string][][]string{
		iptables.ProtocolIPv4: make(map[string]map[string][][]string),
		iptables.ProtocolIPv6: make(map[string]map[string][][]string),
	}

	// Initialize ruleMap with empty chains, so that if no rules are found we will restore the empty chain anyway
	for filterTable, filterChainMap := range filter {
		for filterChain := range filterChainMap {
			if config.IPv4Mode {
				ruleMap[iptables.ProtocolIPv4][filterTable] = map[string][][]string{filterChain: {}}
			}
			if config.IPv6Mode {
				ruleMap[iptables.ProtocolIPv6][filterTable] = map[string][][]string{filterChain: {}}
			}
		}
	}

	// rules can be inserted in groups if they are within the same table
	// sort them into a proper map. Ignore rules that do not pass the filter
	for _, r := range rules {
		if _, ok := filter[r.Table][r.Chain]; !ok {
			klog.V(5).Infof("Ignoring processing rule in table due to filtering: %s, chain: %s with args: \"%s\" for protocol: %v ",
				r.Table, r.Chain, strings.Join(r.Args, " "), r.Protocol)
			continue
		}
		if _, ok := ruleMap[r.Protocol][r.Table]; !ok {
			ruleMap[r.Protocol][r.Table] = make(map[string][][]string)
		}
		ruleMap[r.Protocol][r.Table][r.Chain] = append(ruleMap[r.Protocol][r.Table][r.Chain], r.Args)
	}

	// iterate ip protocol
	for proto, tableMap := range ruleMap {
		// get iptables helper
		if ipt, err = util.GetIPTablesHelper(proto); err != nil {
			err = fmt.Errorf("failed to get iptables helper for protocol %v: %w", proto, err)
			errs = append(errs, err)
			continue
		}

		// iterate table
		for table, chainMap := range tableMap {
			// config map is built for the table, configure everything now
			err = ipt.Restore(table, chainMap)
			if err != nil {
				err = fmt.Errorf("failed to restore iptables group of rules for table %s: %w", table, err)
				errs = append(errs, err)
			}
		}
	}

	return utilerrors.Join(errs...)
}

// AddRules adds the given rules to iptables.
func AddRules(rules []Rule, isAppend bool) error {
	var err error
	var errs []error
	var ipt util.IPTablesHelper
	var exists bool

	// stores valid chains and whether they were already created or not
	// key is ip protocol, table, chain
	createdChains := map[iptables.Protocol]map[string]map[string]bool{
		iptables.ProtocolIPv4: make(map[string]map[string]bool),
		iptables.ProtocolIPv6: make(map[string]map[string]bool),
	}

	for _, r := range rules {
		if ipt, err = util.GetIPTablesHelper(r.Protocol); err != nil {
			err := fmt.Errorf("failed to add iptables %s/%s rule %q: %w", r.Table, r.Chain, strings.Join(r.Args, " "), err)
			errs = append(errs, err)
			continue
		}
		if _, ok := createdChains[r.Protocol][r.Table][r.Chain]; !ok {
			klog.Infof("Creating table: %s chain: %s", r.Table, r.Chain)
			if err = ipt.NewChain(r.Table, r.Chain); err != nil {
				klog.V(5).Infof("Chain: \"%s\" in table: \"%s\" already exists, skipping creation: %v",
					r.Chain, r.Table, err)
			}
			// we assume an error means it was already created
			if _, ok := createdChains[r.Protocol][r.Table]; !ok {
				createdChains[r.Protocol][r.Table] = make(map[string]bool)
			}
			createdChains[r.Protocol][r.Table][r.Chain] = true
		}
		exists, err = ipt.Exists(r.Table, r.Chain, r.Args...)
		if !exists && err == nil {
			klog.V(5).Infof("Adding rule in table: %s, chain: %s with args: \"%s\" for protocol: %v ",
				r.Table, r.Chain, strings.Join(r.Args, " "), r.Protocol)
			if isAppend {
				err = ipt.Append(r.Table, r.Chain, r.Args...)
			} else {
				err = ipt.Insert(r.Table, r.Chain, 1, r.Args...)
			}
		}
		if err != nil {
			err := fmt.Errorf("failed to add iptables %s/%s rule %q: %w", r.Table, r.Chain, strings.Join(r.Args, " "), err)
			errs = append(errs, err)
		}
	}

	return utilerrors.Join(errs...)
}

// DelRules deletes the given rules from iptables.
func DelRules(rules []Rule) error {
	var err error
	var errs []error
	var ipt util.IPTablesHelper
	for _, r := range rules {
		klog.V(5).Infof("Deleting rule in table: %s, chain: %s with args: \"%s\" for protocol: %v ",
			r.Table, r.Chain, strings.Join(r.Args, " "), r.Protocol)
		if ipt, err = util.GetIPTablesHelper(r.Protocol); err != nil {
			err := fmt.Errorf("failed to delete iptables %s/%s rule %q: %w", r.Table, r.Chain, strings.Join(r.Args, " "), err)
			errs = append(errs, err)
			continue
		}
		if exists, err := ipt.Exists(r.Table, r.Chain, r.Args...); err == nil && exists {
			err := ipt.Delete(r.Table, r.Chain, r.Args...)
			if err != nil {
				err := fmt.Errorf("failed to delete iptables %s/%s rule %q: %w", r.Table, r.Chain, strings.Join(r.Args, " "), err)
				errs = append(errs, err)
			}
		}
	}

	return utilerrors.Join(errs...)
}
