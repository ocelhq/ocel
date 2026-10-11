package alb

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/platform/gcp/gateplugin"
)

var gatePluginBytes = []byte("\x00asm-the-preview-gate")

type gateWorld struct {
	mu        sync.Mutex
	events    []string
	uploads   int
	unenforce map[string]int
	probes    map[string]int
	probeErr  error
}

func (w *world) Ensure(_ context.Context, plugin []byte) (string, error) {
	w.gate.mu.Lock()
	defer w.gate.mu.Unlock()
	w.gate.uploads++
	digest := sha256.Sum256(plugin)
	return "projects/acme-prod/locations/europe-west1/repositories/ocel-gate/genericArtifacts/previewgate:" + hex.EncodeToString(digest[:6]), nil
}

func (w *world) probeGate(_ context.Context, hostname, address string) (bool, error) {
	w.gate.mu.Lock()
	defer w.gate.mu.Unlock()
	w.gate.events = append(w.gate.events, "probe "+hostname+" at "+address)
	if w.gate.probes == nil {
		w.gate.probes = map[string]int{}
	}
	w.gate.probes[hostname]++
	if w.gate.probeErr != nil {
		return false, w.gate.probeErr
	}
	if w.gate.unenforce[hostname] > 0 {
		w.gate.unenforce[hostname]--
		return false, nil
	}
	return true, nil
}

func (w *world) leaveUnenforced(hostname string, probes int) {
	w.gate.mu.Lock()
	defer w.gate.mu.Unlock()
	if w.gate.unenforce == nil {
		w.gate.unenforce = map[string]int{}
	}
	w.gate.unenforce[hostname] = probes
}

func (w *world) noteEvent(event string) {
	w.gate.mu.Lock()
	defer w.gate.mu.Unlock()
	w.gate.events = append(w.gate.events, event)
}

func (w *world) happened() []string {
	w.gate.mu.Lock()
	defer w.gate.mu.Unlock()
	return slices.Clone(w.gate.events)
}

func (w *world) probed(hostname string) int {
	w.gate.mu.Lock()
	defer w.gate.mu.Unlock()
	return w.gate.probes[hostname]
}

func (w *world) gateConfig(tierStack string) string {
	for _, declared := range w.declarations(tierStack) {
		if declared.Token != wasmPluginToken {
			continue
		}
		versions, _ := declared.Args["versions"].([]any)
		main, _ := declared.Args["mainVersionId"].(string)
		for _, each := range versions {
			version, _ := each.(map[string]any)
			if version["versionName"] != main {
				continue
			}
			secret, _ := version["pluginConfigData"].(*resource.Secret)
			data := secret.Element.StringValue()
			decoded, err := base64.StdEncoding.DecodeString(data)
			if err != nil {
				return "undecodable: " + err.Error()
			}
			return string(decoded)
		}
	}
	return ""
}

func (w *world) gatedHosts(tierStack string) []string {
	config, err := gateplugin.ParseConfig(w.gateConfig(tierStack))
	if err != nil {
		return []string{"unparseable: " + err.Error()}
	}
	return config.ListHosts()
}

const (
	wasmPluginToken      = "gcp:networkservices/wasmPlugin:WasmPlugin"
	lbEdgeExtensionToken = "gcp:networkservices/lbEdgeExtension:LbEdgeExtension"
)

var errProbeRefused = errors.New("the load balancer refused the connection")

func eventsMentioning(events []string, part string) []string {
	var found []string
	for _, event := range events {
		if strings.Contains(event, part) {
			found = append(found, event)
		}
	}
	return found
}

func pluginConfigIsSecret(program Program) (bool, error) {
	var secret bool
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		return program(ctx, "acme-prod")
	}, pulumi.WithMocks("alb", "test", secretMocks{secret: &secret}))
	return secret, err
}

type secretMocks struct{ secret *bool }

func (m secretMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	if args.TypeToken == wasmPluginToken {
		for _, version := range args.Inputs["versions"].ArrayValue() {
			if version.ObjectValue()["pluginConfigData"].IsSecret() {
				*m.secret = true
			}
		}
	}
	return args.Name + "-id", args.Inputs.Copy(), nil
}

func (secretMocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}
