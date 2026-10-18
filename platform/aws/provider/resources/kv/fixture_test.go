package kv

import "github.com/ocelhq/ocel/platform/aws/provider/component"

var testNetwork = component.Network{VPCID: "vpc-1", VPCCIDR: "10.0.0.0/16", SubnetIDs: []string{"subnet-1", "subnet-2"}}
