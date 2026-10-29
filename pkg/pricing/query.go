package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type PurchaseOption string

const PurchaseOnDemand PurchaseOption = "on_demand"

type Query struct {
	Vendor         string
	Service        string
	Region         string
	ProductFamily  string
	Attributes     map[string]string
	PurchaseOption PurchaseOption
	Unit           string
}

func (q Query) Key() string {
	option := q.PurchaseOption
	if option == "" {
		option = PurchaseOnDemand
	}
	canonical, _ := json.Marshal(struct {
		Vendor         string            `json:"vendor"`
		Service        string            `json:"service"`
		Region         string            `json:"region"`
		ProductFamily  string            `json:"productFamily"`
		Attributes     map[string]string `json:"attributes"`
		PurchaseOption PurchaseOption    `json:"purchaseOption"`
		Unit           string            `json:"unit"`
	}{q.Vendor, q.Service, q.Region, q.ProductFamily, q.Attributes, option, q.Unit})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
