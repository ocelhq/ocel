package offer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

const (
	OnDemandTerm = "JRTCKXETXF"
	Host         = "https://pricing.us-east-1.amazonaws.com"

	readerSize = 1 << 20
)

type Product struct {
	SKU        string
	Attributes map[string]string
}

type Price struct {
	SKU        string
	Unit       string
	BeginRange string
	EndRange   string
	USD        string
}

type Sink struct {
	Product func(Product) error
	Price   func(Price) error
}

func Path(service, region string) string {
	if region == "" {
		return "/offers/v1.0/aws/" + service + "/current/index.json"
	}
	return "/offers/v1.0/aws/" + service + "/current/" + region + "/index.json"
}

func Source(service, version, region string) string {
	if region == "" {
		region = "global"
	}
	return Host + "/offers/v1.0/aws/" + service + "/" + version + "/" + region + "/index.json"
}

func ReadOnDemand(r io.Reader, sink Sink) (string, error) {
	dec := json.NewDecoder(bufio.NewReaderSize(r, readerSize))
	if err := open(dec, '{'); err != nil {
		return "", err
	}
	version := ""
	for dec.More() {
		key, err := name(dec)
		if err != nil {
			return "", err
		}
		switch key {
		case "version":
			if err := dec.Decode(&version); err != nil {
				return "", err
			}
		case "products":
			if err := products(dec, sink.Product); err != nil {
				return "", err
			}
		case "terms":
			if err := terms(dec, sink.Price); err != nil {
				return "", err
			}
		default:
			if err := skip(dec); err != nil {
				return "", err
			}
		}
	}
	return version, shut(dec, '}')
}

func products(dec *json.Decoder, emit func(Product) error) error {
	if err := open(dec, '{'); err != nil {
		return err
	}
	for dec.More() {
		if _, err := name(dec); err != nil {
			return err
		}
		if emit == nil {
			if err := skip(dec); err != nil {
				return err
			}
			continue
		}
		var held struct {
			SKU        string            `json:"sku"`
			Attributes map[string]string `json:"attributes"`
		}
		if err := dec.Decode(&held); err != nil {
			return err
		}
		if err := emit(Product{SKU: held.SKU, Attributes: held.Attributes}); err != nil {
			return err
		}
	}
	return shut(dec, '}')
}

type term struct {
	OfferTermCode   string `json:"offerTermCode"`
	PriceDimensions map[string]struct {
		BeginRange   string            `json:"beginRange"`
		EndRange     string            `json:"endRange"`
		Unit         string            `json:"unit"`
		PricePerUnit map[string]string `json:"pricePerUnit"`
	} `json:"priceDimensions"`
}

func terms(dec *json.Decoder, emit func(Price) error) error {
	if err := open(dec, '{'); err != nil {
		return err
	}
	for dec.More() {
		kind, err := name(dec)
		if err != nil {
			return err
		}
		if kind != "OnDemand" || emit == nil {
			if err := skip(dec); err != nil {
				return err
			}
			continue
		}
		if err := onDemand(dec, emit); err != nil {
			return err
		}
	}
	return shut(dec, '}')
}

func onDemand(dec *json.Decoder, emit func(Price) error) error {
	if err := open(dec, '{'); err != nil {
		return err
	}
	for dec.More() {
		sku, err := name(dec)
		if err != nil {
			return err
		}
		var offered map[string]term
		if err := dec.Decode(&offered); err != nil {
			return err
		}
		for _, held := range offered {
			if held.OfferTermCode != OnDemandTerm {
				continue
			}
			for _, dimension := range held.PriceDimensions {
				if err := emit(Price{
					SKU: sku, Unit: dimension.Unit,
					BeginRange: dimension.BeginRange, EndRange: dimension.EndRange,
					USD: dimension.PricePerUnit["USD"],
				}); err != nil {
					return err
				}
			}
		}
	}
	return shut(dec, '}')
}

func name(dec *json.Decoder) (string, error) {
	token, err := dec.Token()
	if err != nil {
		return "", err
	}
	key, held := token.(string)
	if !held {
		return "", fmt.Errorf("offer file: %v is no object key", token)
	}
	return key, nil
}

func open(dec *json.Decoder, want json.Delim) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if token != want {
		return fmt.Errorf("offer file: %v where %v was due", token, want)
	}
	return nil
}

func shut(dec *json.Decoder, want json.Delim) error { return open(dec, want) }

func skip(dec *json.Decoder) error {
	depth := 0
	for {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		if delim, held := token.(json.Delim); held {
			if delim == '{' || delim == '[' {
				depth++
			} else {
				depth--
			}
		}
		if depth == 0 {
			return nil
		}
	}
}
