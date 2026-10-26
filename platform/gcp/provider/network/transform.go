package network

import (
	"cmp"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/networkconnectivity/v1"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

const (
	TransformKind = "network"

	networkSurfaceName    = "network"
	subnetworkSurfaceName = "subnetwork"
	policySurfaceName     = "connectionPolicy"

	minMTU = 1300
	maxMTU = 8896

	widestRangeBits    = 16
	narrowestRangeBits = 26

	defaultFlowInterval = "INTERVAL_5_SEC"
	defaultFlowSampling = 0.5
	defaultFlowMetadata = "INCLUDE_ALL_METADATA"
	defaultFlowFilter   = "true"
)

var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

var networkSurface = surface.Surface[compute.Network, compute.Network]{
	Name: networkSurfaceName,
	Fields: []surface.Field[compute.Network, compute.Network]{
		{
			Name: "MTU",
			Paths: []surface.Path[compute.Network]{surface.NewPath("mtu", func(desired *compute.Network, mtu int64) error {
				if mtu < minMTU || mtu > maxMTU {
					return fmt.Errorf("%d is outside %d to %d", mtu, minMTU, maxMTU)
				}
				desired.Mtu = mtu
				return nil
			})},
		},
	},
}

type subnetworkUpdate struct {
	fields                compute.Subnetwork
	privateIpGoogleAccess *bool
}

func (u subnetworkUpdate) hasFields() bool { return len(u.fields.ForceSendFields) > 0 }

func (u *subnetworkUpdate) send(field string) {
	if !slices.Contains(u.fields.ForceSendFields, field) {
		u.fields.ForceSendFields = append(u.fields.ForceSendFields, field)
	}
}

type pulumiFlowLogs struct {
	AggregationInterval string   `json:"aggregationInterval"`
	FilterExpr          string   `json:"filterExpr"`
	FlowSampling        *float64 `json:"flowSampling"`
	Metadata            string   `json:"metadata"`
	MetadataFields      []string `json:"metadataFields"`
}

type pulumiSecondaryRange struct {
	RangeName   string `json:"rangeName"`
	IpCidrRange string `json:"ipCidrRange"`
}

var subnetworkSurface = surface.Surface[compute.Subnetwork, subnetworkUpdate]{
	Name: subnetworkSurfaceName,
	Fields: []surface.Field[compute.Subnetwork, subnetworkUpdate]{
		{
			Name: "range",
			Paths: []surface.Path[compute.Subnetwork]{surface.NewPath("ipCidrRange", func(desired *compute.Subnetwork, block string) error {
				if err := refuseUnusableRange(block); err != nil {
					return err
				}
				desired.IpCidrRange = block
				return nil
			})},
		},
		{
			Name: "private Google access",
			Paths: []surface.Path[compute.Subnetwork]{surface.NewPath("privateIpGoogleAccess", func(desired *compute.Subnetwork, on bool) error {
				desired.PrivateIpGoogleAccess = on
				return nil
			})},
			Reset: func(desired *compute.Subnetwork) { desired.PrivateIpGoogleAccess = false },
			Same: func(current, desired *compute.Subnetwork) bool {
				return current.PrivateIpGoogleAccess == desired.PrivateIpGoogleAccess
			},
			Update: func(update *subnetworkUpdate, _, desired *compute.Subnetwork) {
				on := desired.PrivateIpGoogleAccess
				update.privateIpGoogleAccess = &on
			},
			Show: func(subnet *compute.Subnetwork) string { return onOff(subnet.PrivateIpGoogleAccess) },
		},
		{
			Name: "flow logs",
			Paths: []surface.Path[compute.Subnetwork]{surface.NewPath("logConfig", func(desired *compute.Subnetwork, logs pulumiFlowLogs) error {
				translated, err := logs.translate()
				if err != nil {
					return err
				}
				desired.LogConfig = translated
				return nil
			})},
			Reset: func(desired *compute.Subnetwork) { desired.LogConfig = nil },
			Same: func(current, desired *compute.Subnetwork) bool {
				return sameFlowLogs(flowLogsOf(current), flowLogsOf(desired))
			},
			Update: func(update *subnetworkUpdate, _, desired *compute.Subnetwork) {
				logs := flowLogsOf(desired)
				logs.ForceSendFields = append(slices.Clone(logs.ForceSendFields), "Enable")
				update.fields.LogConfig = &logs
				update.send("LogConfig")
			},
			Show: func(subnet *compute.Subnetwork) string { return onOff(flowLogsOf(subnet).Enable) },
		},
		{
			Name: "secondary ranges",
			Paths: []surface.Path[compute.Subnetwork]{surface.NewPath("secondaryIpRanges", func(desired *compute.Subnetwork, ranges []pulumiSecondaryRange) error {
				for _, each := range ranges {
					if each.RangeName == "" {
						return fmt.Errorf("a secondary range names no rangeName")
					}
					if err := refuseUnusableRange(each.IpCidrRange); err != nil {
						return fmt.Errorf("secondary range %s: %w", each.RangeName, err)
					}
					desired.SecondaryIpRanges = append(desired.SecondaryIpRanges, &compute.SubnetworkSecondaryRange{RangeName: each.RangeName, IpCidrRange: each.IpCidrRange})
				}
				return nil
			})},
			Reset: func(desired *compute.Subnetwork) { desired.SecondaryIpRanges = nil },
			Same: func(current, desired *compute.Subnetwork) bool {
				return slices.Equal(secondaryRangesOf(current), secondaryRangesOf(desired))
			},
			Update: func(update *subnetworkUpdate, _, desired *compute.Subnetwork) {
				update.fields.SecondaryIpRanges = desired.SecondaryIpRanges
				if update.fields.SecondaryIpRanges == nil {
					update.fields.SecondaryIpRanges = []*compute.SubnetworkSecondaryRange{}
				}
				update.send("SecondaryIpRanges")
			},
			Show: func(subnet *compute.Subnetwork) string {
				if ranges := secondaryRangesOf(subnet); len(ranges) > 0 {
					return strings.Join(ranges, ", ")
				}
				return "none"
			},
		},
	},
}

func refuseUnusableRange(block string) error {
	prefix, err := netip.ParsePrefix(block)
	if err != nil || !prefix.Addr().Is4() || prefix.Masked() != prefix {
		return fmt.Errorf("%q is not an IPv4 CIDR block such as %s", block, SubnetRange)
	}
	if prefix.Bits() < widestRangeBits || prefix.Bits() > narrowestRangeBits {
		return fmt.Errorf("%s is a /%d, and the subnetwork takes a /%d to a /%d", block, prefix.Bits(), widestRangeBits, narrowestRangeBits)
	}
	if !slices.ContainsFunc(privateRanges, func(private netip.Prefix) bool {
		return private.Bits() <= prefix.Bits() && private.Contains(prefix.Addr())
	}) {
		return fmt.Errorf("%s lies outside the private ranges 10.0.0.0/8, 172.16.0.0/12 and 192.168.0.0/16", block)
	}
	return nil
}

func (l pulumiFlowLogs) translate() (*compute.SubnetworkLogConfig, error) {
	logs := &compute.SubnetworkLogConfig{
		Enable:              true,
		AggregationInterval: cmp.Or(l.AggregationInterval, defaultFlowInterval),
		FilterExpr:          cmp.Or(l.FilterExpr, defaultFlowFilter),
		FlowSampling:        defaultFlowSampling,
		Metadata:            cmp.Or(l.Metadata, defaultFlowMetadata),
		MetadataFields:      l.MetadataFields,
	}
	if l.FlowSampling != nil {
		if *l.FlowSampling < 0 || *l.FlowSampling > 1 {
			return nil, fmt.Errorf("flowSampling %v is outside 0 to 1", *l.FlowSampling)
		}
		logs.FlowSampling = *l.FlowSampling
		logs.ForceSendFields = []string{"FlowSampling"}
	}
	return logs, nil
}

func flowLogsOf(subnet *compute.Subnetwork) compute.SubnetworkLogConfig {
	if subnet.LogConfig == nil || !subnet.LogConfig.Enable {
		return compute.SubnetworkLogConfig{}
	}
	return *subnet.LogConfig
}

func sameFlowLogs(current, desired compute.SubnetworkLogConfig) bool {
	if !current.Enable || !desired.Enable {
		return current.Enable == desired.Enable
	}
	return cmp.Or(current.AggregationInterval, defaultFlowInterval) == desired.AggregationInterval &&
		cmp.Or(current.FilterExpr, defaultFlowFilter) == desired.FilterExpr &&
		current.FlowSampling == desired.FlowSampling &&
		cmp.Or(current.Metadata, defaultFlowMetadata) == desired.Metadata &&
		slices.Equal(current.MetadataFields, desired.MetadataFields)
}

func secondaryRangesOf(subnet *compute.Subnetwork) []string {
	ranges := make([]string, 0, len(subnet.SecondaryIpRanges))
	for _, each := range subnet.SecondaryIpRanges {
		ranges = append(ranges, each.RangeName+" "+each.IpCidrRange)
	}
	slices.Sort(ranges)
	return ranges
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

type policyUpdate struct {
	policy networkconnectivity.ServiceConnectionPolicy
	mask   []string
}

func newPolicyUpdate(current *networkconnectivity.ServiceConnectionPolicy) policyUpdate {
	return policyUpdate{policy: networkconnectivity.ServiceConnectionPolicy{Labels: maps.Clone(current.Labels)}}
}

var policySurface = surface.Surface[networkconnectivity.ServiceConnectionPolicy, policyUpdate]{
	Name: policySurfaceName,
	Fields: []surface.Field[networkconnectivity.ServiceConnectionPolicy, policyUpdate]{
		{
			Name: "connection limit",
			Paths: []surface.Path[networkconnectivity.ServiceConnectionPolicy]{surface.NewPath("pscConfig.limit", func(desired *networkconnectivity.ServiceConnectionPolicy, limit string) error {
				connections, err := strconv.ParseInt(limit, 10, 64)
				if err != nil || connections <= 0 {
					return fmt.Errorf("%q is not a whole number of connections above zero", limit)
				}
				desired.PscConfig.Limit = connections
				return nil
			})},
			Reset: func(desired *networkconnectivity.ServiceConnectionPolicy) { desired.PscConfig.Limit = 0 },
			Same: func(current, desired *networkconnectivity.ServiceConnectionPolicy) bool {
				return connectionLimit(current) == connectionLimit(desired)
			},
			Update: func(update *policyUpdate, _, desired *networkconnectivity.ServiceConnectionPolicy) {
				update.policy.PscConfig = &networkconnectivity.PscConfig{Limit: connectionLimit(desired), ForceSendFields: []string{"Limit"}}
				update.mask = append(update.mask, "pscConfig.limit")
			},
			Show: func(policy *networkconnectivity.ServiceConnectionPolicy) string {
				if limit := connectionLimit(policy); limit > 0 {
					return strconv.FormatInt(limit, 10)
				}
				return "no limit"
			},
		},
	},
	Labels: &surface.Labels[networkconnectivity.ServiceConnectionPolicy, policyUpdate]{
		Of: func(policy *networkconnectivity.ServiceConnectionPolicy) *map[string]string { return &policy.Labels },
		Update: func(update *policyUpdate, set map[string]string, removed []string) {
			if update.policy.Labels == nil {
				update.policy.Labels = map[string]string{}
			}
			maps.Copy(update.policy.Labels, set)
			for _, key := range removed {
				delete(update.policy.Labels, key)
			}
			update.policy.ForceSendFields = append(update.policy.ForceSendFields, "Labels")
			update.mask = append(update.mask, "labels")
		},
	},
}

func connectionLimit(policy *networkconnectivity.ServiceConnectionPolicy) int64 {
	if policy.PscConfig == nil {
		return 0
	}
	return policy.PscConfig.Limit
}

func Allowed() surface.Allowed {
	return surface.Allowed{
		networkSurfaceName:    networkSurface.Paths(),
		subnetworkSurfaceName: subnetworkSurface.Paths(),
		policySurfaceName:     policySurface.Paths(),
	}
}
