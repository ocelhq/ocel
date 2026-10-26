package kv

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

const (
	transformKind    = "kv"
	transformSurface = "instance"
	zonesPath        = "zoneDistributionConfig"

	multiZone       = "MULTI_ZONE"
	backupsEnabled  = "ENABLED"
	backupsDisabled = "DISABLED"
	maxReplicas     = 5
)

type instanceUpdate struct {
	mask          []string
	changed       gcloud.MemorystoreInstance
	currentLabels map[string]string
}

func (u *instanceUpdate) send(field string) {
	if !slices.Contains(u.mask, field) {
		u.mask = append(u.mask, field)
	}
}

type storeField = surface.Field[gcloud.MemorystoreInstance, instanceUpdate]

type pulumiTimeOfDay struct {
	Hours   int `json:"hours"`
	Minutes int `json:"minutes"`
	Seconds int `json:"seconds"`
	Nanos   int `json:"nanos"`
}

type pulumiMaintenanceWindow struct {
	Day       string          `json:"day"`
	StartTime pulumiTimeOfDay `json:"startTime"`
}

type pulumiAutomatedBackups struct {
	FixedFrequencySchedule struct {
		StartTime struct {
			Hours int `json:"hours"`
		} `json:"startTime"`
	} `json:"fixedFrequencySchedule"`
	Retention string `json:"retention"`
}

type pulumiEndpoint struct {
	Network string `json:"network"`
}

var storeSurface = surface.Surface[gcloud.MemorystoreInstance, instanceUpdate]{
	Name: transformSurface,
	Fields: []storeField{
		{
			Name: "replica count",
			Paths: []surface.Path[gcloud.MemorystoreInstance]{surface.NewPath("replicaCount", func(desired *gcloud.MemorystoreInstance, replicas int) error {
				if replicas < 0 || replicas > maxReplicas {
					return fmt.Errorf("%d replicas is outside the 0 to %d Memorystore runs", replicas, maxReplicas)
				}
				desired.ReplicaCount = &replicas
				return nil
			})},
		},
		{
			Name: "zone distribution",
			Paths: []surface.Path[gcloud.MemorystoreInstance]{surface.NewPath(zonesPath, func(desired *gcloud.MemorystoreInstance, zones gcloud.ZoneDistribution) error {
				desired.ZoneDistributionConfig = &zones
				return nil
			})},
		},
		{
			Name: "maintenance policy",
			Paths: []surface.Path[gcloud.MemorystoreInstance]{surface.NewPath("maintenancePolicy.weeklyMaintenanceWindows", func(desired *gcloud.MemorystoreInstance, windows []pulumiMaintenanceWindow) error {
				policy := &gcloud.MaintenancePolicy{}
				for _, window := range windows {
					start := gcloud.TimeOfDay(window.StartTime)
					policy.WeeklyMaintenanceWindow = append(policy.WeeklyMaintenanceWindow, gcloud.WeeklyMaintenanceWindow{Day: window.Day, StartTime: &start})
				}
				desired.MaintenancePolicy = policy
				return nil
			})},
			Same: func(current, desired *gcloud.MemorystoreInstance) bool {
				return slices.Equal(listWindows(current), listWindows(desired))
			},
			Update: func(update *instanceUpdate, _, desired *gcloud.MemorystoreInstance) {
				update.changed.MaintenancePolicy = desired.MaintenancePolicy
				update.send("maintenance_policy")
			},
			Show: func(instance *gcloud.MemorystoreInstance) string {
				if windows := listWindows(instance); len(windows) > 0 {
					return strings.Join(windows, ", ")
				}
				return "any window"
			},
		},
		{
			Name: "automated backups",
			Paths: []surface.Path[gcloud.MemorystoreInstance]{surface.NewPath("automatedBackupConfig", func(desired *gcloud.MemorystoreInstance, backups pulumiAutomatedBackups) error {
				seconds, cut := strings.CutSuffix(backups.Retention, "s")
				if _, err := strconv.ParseInt(seconds, 10, 64); !cut || err != nil {
					return fmt.Errorf("retention %q is not a whole number of seconds, such as 604800s", backups.Retention)
				}
				desired.AutomatedBackupConfig = &gcloud.AutomatedBackups{
					AutomatedBackupMode:    backupsEnabled,
					FixedFrequencySchedule: &gcloud.FixedFrequencyBackup{StartTime: &gcloud.TimeOfDay{Hours: backups.FixedFrequencySchedule.StartTime.Hours}},
					Retention:              backups.Retention,
				}
				return nil
			})},
			Same: func(current, desired *gcloud.MemorystoreInstance) bool {
				return describeBackups(current) == describeBackups(desired)
			},
			Update: func(update *instanceUpdate, _, desired *gcloud.MemorystoreInstance) {
				update.changed.AutomatedBackupConfig = desired.AutomatedBackupConfig
				if update.changed.AutomatedBackupConfig == nil {
					update.changed.AutomatedBackupConfig = &gcloud.AutomatedBackups{AutomatedBackupMode: backupsDisabled}
				}
				update.send("automated_backup_config")
			},
			Show: describeBackups,
		},
		{
			Name: "deletion protection",
			Paths: []surface.Path[gcloud.MemorystoreInstance]{surface.NewPath("deletionProtectionEnabled", func(desired *gcloud.MemorystoreInstance, protected bool) error {
				desired.DeletionProtectionEnabled = &protected
				return nil
			})},
			Same: func(current, desired *gcloud.MemorystoreInstance) bool {
				return isProtected(current) == isProtected(desired)
			},
			Update: func(update *instanceUpdate, _, desired *gcloud.MemorystoreInstance) {
				protected := isProtected(desired)
				update.changed.DeletionProtectionEnabled = &protected
				update.send("deletion_protection_enabled")
			},
			Show: func(instance *gcloud.MemorystoreInstance) string { return surface.ShowOnOff(isProtected(instance)) },
		},
		{
			Name: "endpoint network",
			Paths: []surface.Path[gcloud.MemorystoreInstance]{surface.NewPath(surface.StorePlacement, func(desired *gcloud.MemorystoreInstance, endpoints []pulumiEndpoint) error {
				if len(endpoints) != 1 {
					return fmt.Errorf("a store takes one desiredAutoCreatedEndpoints entry, the network its endpoint is made in")
				}
				own := connectionOf(desired)
				network, err := gcloud.PlacedNetworkPath(own.ProjectID, endpoints[0].Network)
				if err != nil {
					return err
				}
				desired.Endpoints = []gcloud.InstanceEndpoint{{Connections: []gcloud.EndpointConnection{
					{PSCAutoConnection: &gcloud.PSCAutoConnection{ProjectID: own.ProjectID, Network: network}},
				}}}
				return nil
			})},
		},
	},
	Labels: &surface.Labels[gcloud.MemorystoreInstance, instanceUpdate]{
		Of: func(instance *gcloud.MemorystoreInstance) *map[string]string { return &instance.Labels },
		Update: func(update *instanceUpdate, set map[string]string, removed []string) {
			labels := maps.Clone(update.currentLabels)
			if labels == nil {
				labels = map[string]string{}
			}
			maps.Copy(labels, set)
			for _, key := range removed {
				delete(labels, key)
			}
			update.changed.Labels = labels
			update.send("labels")
		},
	},
}

func listWindows(instance *gcloud.MemorystoreInstance) []string {
	if instance.MaintenancePolicy == nil {
		return nil
	}
	var windows []string
	for _, window := range instance.MaintenancePolicy.WeeklyMaintenanceWindow {
		start := gcloud.TimeOfDay{}
		if window.StartTime != nil {
			start = *window.StartTime
		}
		windows = append(windows, fmt.Sprintf("%s %02d:%02d", window.Day, start.Hours, start.Minutes))
	}
	return windows
}

func describeBackups(instance *gcloud.MemorystoreInstance) string {
	backups := instance.AutomatedBackupConfig
	if backups == nil || backups.AutomatedBackupMode != backupsEnabled {
		return "off"
	}
	hours := 0
	if backups.FixedFrequencySchedule != nil && backups.FixedFrequencySchedule.StartTime != nil {
		hours = backups.FixedFrequencySchedule.StartTime.Hours
	}
	return fmt.Sprintf("daily at %02d:00 kept %s", hours, backups.Retention)
}

func isProtected(instance *gcloud.MemorystoreInstance) bool {
	return instance.DeletionProtectionEnabled != nil && *instance.DeletionProtectionEnabled
}

func zonesOf(instance *gcloud.MemorystoreInstance) gcloud.ZoneDistribution {
	if zones := instance.ZoneDistributionConfig; zones != nil && zones.Mode != "" {
		return *zones
	}
	return gcloud.ZoneDistribution{Mode: multiZone}
}

func describeZones(zones gcloud.ZoneDistribution) string {
	if zones.Zone == "" {
		return zones.Mode
	}
	return zones.Mode + " in " + zones.Zone
}

func connectionOf(instance *gcloud.MemorystoreInstance) gcloud.PSCAutoConnection {
	for _, endpoint := range instance.Endpoints {
		for _, connection := range endpoint.Connections {
			if connection.PSCAutoConnection != nil {
				return *connection.PSCAutoConnection
			}
		}
	}
	return gcloud.PSCAutoConnection{}
}

func transformTarget() *component.Transform {
	return &component.Transform{Kind: transformKind, Allowed: surface.Allowed{transformSurface: storeSurface.Paths()}}
}
