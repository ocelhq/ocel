package kv

import "github.com/ocelhq/ocel/platform/aws/provider/component"

var testNetwork = component.Network{VPCID: "vpc-1", SubnetIDs: []string{"subnet-1", "subnet-2"}, Clients: "sg-vpc-clients"}
