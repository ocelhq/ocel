package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"math/big"
	"slices"
	"strings"
	"time"

	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const (
	ServingLabel     = "ocel-serving"
	servingDigestLen = 32
	unreadable       = "unreadable:"
)

type servingPrint struct {
	Image                string
	ServiceAccount       string
	Ingress              string
	Timeout              string
	ExecutionEnvironment string
	InvokerIAMDisabled   bool
	IAPEnabled           bool
	CPUIdle              bool
	Limits               map[string]string
	MinInstances         int64
	MaxInstances         int64
	Concurrency          int64
	Env                  [][2]string
	Volumes              [][4]string
	Mounts               [][2]string
	Audiences            []string
	Egress               string
	Networks             [][2]string
	Containers           int
	Tier                 string
}

func Drifted(current, desired *run.GoogleCloudRunV2Service) bool {
	want := desired.Labels[ServingLabel]
	return current.Labels[ServingLabel] != want || digestOf(printOf(current).within(printOf(desired))) != want
}

func stampServing(service *run.GoogleCloudRunV2Service) {
	if service.Labels == nil {
		service.Labels = map[string]string{}
	}
	service.Labels[ServingLabel] = digestOf(printOf(service))
}

func digestOf(p servingPrint) string {
	encoded, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])[:servingDigestLen]
}

func (p servingPrint) within(want servingPrint) servingPrint {
	if want.ServiceAccount == "" {
		p.ServiceAccount = ""
	}
	if want.Timeout == "" {
		p.Timeout = ""
	}
	if want.ExecutionEnvironment == "" {
		p.ExecutionEnvironment = ""
	}
	if want.MaxInstances == 0 {
		p.MaxInstances = 0
	}
	if want.Concurrency == 0 {
		p.Concurrency = 0
	}
	return p
}

func printOf(service *run.GoogleCloudRunV2Service) servingPrint {
	p := servingPrint{
		Ingress:            service.Ingress,
		InvokerIAMDisabled: service.InvokerIamDisabled,
		IAPEnabled:         service.IapEnabled,
		Audiences:          slices.Clone(service.CustomAudiences),
	}
	template := service.Template
	if template == nil {
		return p
	}
	p.ServiceAccount = template.ServiceAccount
	p.Tier = template.Labels[names.TierLabel]
	p.Timeout = durationOf(template.Timeout)
	p.ExecutionEnvironment = template.ExecutionEnvironment
	p.Concurrency = template.MaxInstanceRequestConcurrency
	if template.Scaling != nil {
		p.MinInstances, p.MaxInstances = template.Scaling.MinInstanceCount, template.Scaling.MaxInstanceCount
	}
	for _, volume := range template.Volumes {
		if volume.Secret == nil {
			p.Volumes = append(p.Volumes, [4]string{volume.Name})
			continue
		}
		for _, item := range volume.Secret.Items {
			p.Volumes = append(p.Volumes, [4]string{volume.Name, RevisionName(volume.Secret.Secret), item.Version, item.Path})
		}
	}
	if template.VpcAccess != nil {
		p.Egress = template.VpcAccess.Egress
		for _, network := range template.VpcAccess.NetworkInterfaces {
			p.Networks = append(p.Networks, [2]string{RevisionName(network.Network), RevisionName(network.Subnetwork)})
		}
	}
	p.Containers = len(template.Containers)
	if len(template.Containers) == 0 {
		return p
	}
	container := template.Containers[0]
	p.Image = container.Image
	for _, env := range container.Env {
		p.Env = append(p.Env, [2]string{env.Name, env.Value})
	}
	for _, mount := range container.VolumeMounts {
		p.Mounts = append(p.Mounts, [2]string{mount.Name, mount.MountPath})
	}
	if container.Resources != nil {
		p.CPUIdle = container.Resources.CpuIdle
		p.Limits = map[string]string{}
		for _, name := range slices.Sorted(maps.Keys(container.Resources.Limits)) {
			p.Limits[name] = quantityOf(container.Resources.Limits[name])
		}
	}
	return p
}

func durationOf(written string) string {
	if written == "" {
		return ""
	}
	parsed, err := time.ParseDuration(written)
	if err != nil {
		return unreadable + written
	}
	return parsed.String()
}

var quantitySuffixes = []struct {
	suffix string
	scale  *big.Rat
}{
	{"Ki", new(big.Rat).SetInt64(1 << 10)},
	{"Mi", new(big.Rat).SetInt64(1 << 20)},
	{"Gi", new(big.Rat).SetInt64(1 << 30)},
	{"Ti", new(big.Rat).SetInt64(1 << 40)},
	{"m", big.NewRat(1, 1000)},
	{"k", new(big.Rat).SetInt64(1e3)},
	{"M", new(big.Rat).SetInt64(1e6)},
	{"G", new(big.Rat).SetInt64(1e9)},
	{"T", new(big.Rat).SetInt64(1e12)},
}

func quantityOf(written string) string {
	number, scale := written, big.NewRat(1, 1)
	for _, each := range quantitySuffixes {
		if cut, found := strings.CutSuffix(written, each.suffix); found {
			number, scale = cut, each.scale
			break
		}
	}
	amount, ok := new(big.Rat).SetString(number)
	if !ok {
		return unreadable + written
	}
	return amount.Mul(amount, scale).RatString()
}
