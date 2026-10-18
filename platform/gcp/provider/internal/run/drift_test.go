package run

import (
	"encoding/json"
	"testing"
	"time"

	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/provider"
)

func declared(t *testing.T) *runv2.GoogleCloudRunV2Service {
	t.Helper()
	service, err := ServiceOf(Serving{
		Service:     "ocel-production-envsourcesync",
		Image:       "europe-west1-docker.pkg.dev/acme-prod/ocel/sync@sha256:abc",
		Env:         map[string]string{"OCEL_TIER": "production"},
		Account:     "sync@acme-prod.iam.gserviceaccount.com",
		CPU:         "0.08",
		Memory:      128,
		Concurrency: 1,
		Generation:  "EXECUTION_ENVIRONMENT_GEN1",
		Instances:   provider.Instances{Max: 1},
		Timeout:     time.Minute,
		Ingress:     IngressInternal,
		Audiences:   []string{"https://sync"},
		Mounts:      []SecretMount{{Name: "keys", Secret: "projects/acme-prod/secrets/keys", Dir: "/keys", File: "keys.json"}},
		Egress:      &PrivateEgress{Network: "projects/acme-prod/global/networks/ocel-production", Subnetwork: "projects/acme-prod/regions/europe-west1/subnetworks/ocel-production"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func readBack(t *testing.T, service *runv2.GoogleCloudRunV2Service) *runv2.GoogleCloudRunV2Service {
	t.Helper()
	encoded, err := json.Marshal(service)
	if err != nil {
		t.Fatal(err)
	}
	var read runv2.GoogleCloudRunV2Service
	if err := json.Unmarshal(encoded, &read); err != nil {
		t.Fatal(err)
	}
	return &read
}

func TestAServiceReadBackAsItWasDeclaredHasNotDrifted(t *testing.T) {
	t.Parallel()
	desired := declared(t)
	if desired.Labels[ServingLabel] == "" {
		t.Fatalf("the declared service carries no %s label: %v", ServingLabel, desired.Labels)
	}
	if Drifted(readBack(t, desired), desired) {
		t.Error("a service Cloud Run reads back as it was declared drifted")
	}
}

func TestAServiceCloudRunReadsBackInAnotherNotationHasNotDrifted(t *testing.T) {
	t.Parallel()
	for name, renote := range map[string]func(*runv2.GoogleCloudRunV2Service){
		"cpu in millis":      func(s *runv2.GoogleCloudRunV2Service) { s.Template.Containers[0].Resources.Limits["cpu"] = "80m" },
		"cpu in an exponent": func(s *runv2.GoogleCloudRunV2Service) { s.Template.Containers[0].Resources.Limits["cpu"] = "8e-2" },
		"memory in bytes": func(s *runv2.GoogleCloudRunV2Service) {
			s.Template.Containers[0].Resources.Limits["memory"] = "134217728"
		},
		"memory in kibibytes": func(s *runv2.GoogleCloudRunV2Service) {
			s.Template.Containers[0].Resources.Limits["memory"] = "131072Ki"
		},
		"timeout with millis": func(s *runv2.GoogleCloudRunV2Service) { s.Template.Timeout = "60.000s" },
		"timeout in minutes":  func(s *runv2.GoogleCloudRunV2Service) { s.Template.Timeout = "1m0s" },
		"secret by number": func(s *runv2.GoogleCloudRunV2Service) {
			s.Template.Volumes[0].Secret.Secret = "projects/123/secrets/keys"
		},
		"network by its number": func(s *runv2.GoogleCloudRunV2Service) {
			s.Template.VpcAccess.NetworkInterfaces[0].Network = "projects/123/global/networks/ocel-production"
		},
		"server-filled defaults": func(s *runv2.GoogleCloudRunV2Service) { s.Template.Containers[0].Ports[0].Name = "http1" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			desired := declared(t)
			current := readBack(t, desired)
			renote(current)
			if Drifted(current, desired) {
				t.Errorf("a service read back with %s drifted, and it is what ocel declared", name)
			}
		})
	}
}

func TestAServiceChangedOutsideOcelHasDrifted(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(*runv2.GoogleCloudRunV2Service){
		"image": func(s *runv2.GoogleCloudRunV2Service) { s.Template.Containers[0].Image += "0" },
		"billing": func(s *runv2.GoogleCloudRunV2Service) {
			s.Template.Containers[0].Resources.CpuIdle = !s.Template.Containers[0].Resources.CpuIdle
		},
		"warm instances":   func(s *runv2.GoogleCloudRunV2Service) { s.Template.Scaling.MinInstanceCount = 1 },
		"instance ceiling": func(s *runv2.GoogleCloudRunV2Service) { s.Template.Scaling.MaxInstanceCount = 3 },
		"cpu":              func(s *runv2.GoogleCloudRunV2Service) { s.Template.Containers[0].Resources.Limits["cpu"] = "1" },
		"memory unit":      func(s *runv2.GoogleCloudRunV2Service) { s.Template.Containers[0].Resources.Limits["memory"] = "128M" },
		"a gpu": func(s *runv2.GoogleCloudRunV2Service) {
			s.Template.Containers[0].Resources.Limits["nvidia.com/gpu"] = "1"
		},
		"unreadable limit":     func(s *runv2.GoogleCloudRunV2Service) { s.Template.Containers[0].Resources.Limits["cpu"] = "a lot" },
		"ingress":              func(s *runv2.GoogleCloudRunV2Service) { s.Ingress = IngressEverywhere },
		"invoker check":        func(s *runv2.GoogleCloudRunV2Service) { s.InvokerIamDisabled = true },
		"timeout":              func(s *runv2.GoogleCloudRunV2Service) { s.Template.Timeout = "300s" },
		"timeout gone":         func(s *runv2.GoogleCloudRunV2Service) { s.Template.Timeout = "" },
		"unreadable timeout":   func(s *runv2.GoogleCloudRunV2Service) { s.Template.Timeout = "a minute" },
		"cpu a milli over":     func(s *runv2.GoogleCloudRunV2Service) { s.Template.Containers[0].Resources.Limits["cpu"] = "81m" },
		"memory gone":          func(s *runv2.GoogleCloudRunV2Service) { delete(s.Template.Containers[0].Resources.Limits, "memory") },
		"concurrency":          func(s *runv2.GoogleCloudRunV2Service) { s.Template.MaxInstanceRequestConcurrency = 80 },
		"execution generation": func(s *runv2.GoogleCloudRunV2Service) { s.Template.ExecutionEnvironment = "EXECUTION_ENVIRONMENT_GEN2" },
		"account": func(s *runv2.GoogleCloudRunV2Service) {
			s.Template.ServiceAccount = "other@acme-prod.iam.gserviceaccount.com"
		},
		"environment": func(s *runv2.GoogleCloudRunV2Service) { s.Template.Containers[0].Env[0].Value = "preview" },
		"mount": func(s *runv2.GoogleCloudRunV2Service) {
			s.Template.Containers[0].VolumeMounts[0].MountPath = "/elsewhere"
		},
		"secret version":   func(s *runv2.GoogleCloudRunV2Service) { s.Template.Volumes[0].Secret.Items[0].Version = "1" },
		"audiences":        func(s *runv2.GoogleCloudRunV2Service) { s.CustomAudiences = nil },
		"iap":              func(s *runv2.GoogleCloudRunV2Service) { s.IapEnabled = true },
		"egress":           func(s *runv2.GoogleCloudRunV2Service) { s.Template.VpcAccess.Egress = "ALL_TRAFFIC" },
		"no network":       func(s *runv2.GoogleCloudRunV2Service) { s.Template.VpcAccess = nil },
		"no serving label": func(s *runv2.GoogleCloudRunV2Service) { delete(s.Labels, ServingLabel) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			desired := declared(t)
			current := readBack(t, desired)
			change(current)
			if !Drifted(current, desired) {
				t.Errorf("a service whose %s changed outside ocel reads as current", name)
			}
		})
	}
}

func TestAServiceOcelNowDeclaresWithoutASettingCloudRunDefaultsHasDrifted(t *testing.T) {
	t.Parallel()
	current := readBack(t, declared(t))
	serving := Serving{Service: "ocel-production-envsourcesync", Image: current.Template.Containers[0].Image}
	desired, err := ServiceOf(serving)
	if err != nil {
		t.Fatal(err)
	}
	current.Template.Containers[0].Env = nil
	current.Template.Volumes, current.Template.Containers[0].VolumeMounts = nil, nil
	current.Template.VpcAccess, current.CustomAudiences = nil, nil
	current.Template.Containers[0].Resources.Limits = desired.Template.Containers[0].Resources.Limits
	current.Ingress = desired.Ingress
	if !Drifted(current, desired) {
		t.Error("a service still running the concurrency, timeout and generation ocel no longer declares reads as current: Cloud Run would keep them, so only the label shows the declaration changed")
	}
}
