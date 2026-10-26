package postgres

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	sqladmin "google.golang.org/api/sqladmin/v1"

	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

const (
	transformKind    = "postgres"
	transformSurface = "instance"
	diskTypePath     = "settings.diskType"

	cloudSQLEnterprisePlus      = "ENTERPRISE_PLUS"
	defaultRetainedBackups      = 7
	defaultRetentionUnit        = "COUNT"
	defaultLogRetentionDays     = 7
	defaultPlusLogRetentionDays = 14
)

type instanceField = surface.Field[sqladmin.DatabaseInstance, sqladmin.DatabaseInstance]

type pulumiPSCConfig struct {
	PscAutoConnections []struct {
		ConsumerNetwork string `json:"consumerNetwork"`
	} `json:"pscAutoConnections"`
}

type pulumiDatabaseFlag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type pulumiMaintenanceWindow struct {
	Day         int64  `json:"day"`
	Hour        int64  `json:"hour"`
	UpdateTrack string `json:"updateTrack"`
}

type pulumiInsightsConfig struct {
	QueryInsightsEnabled  bool  `json:"queryInsightsEnabled"`
	QueryPlansPerMinute   int64 `json:"queryPlansPerMinute"`
	QueryStringLength     int64 `json:"queryStringLength"`
	RecordApplicationTags bool  `json:"recordApplicationTags"`
	RecordClientAddress   bool  `json:"recordClientAddress"`
}

type pulumiRetentionSettings struct {
	RetainedBackups int64  `json:"retainedBackups"`
	RetentionUnit   string `json:"retentionUnit"`
}

func setting[V any](name string, set func(settings *sqladmin.Settings, value V)) []surface.Path[sqladmin.DatabaseInstance] {
	return []surface.Path[sqladmin.DatabaseInstance]{surface.NewPath("settings."+name, func(desired *sqladmin.DatabaseInstance, value V) error {
		set(desired.Settings, value)
		return nil
	})}
}

func backupSetting[V any](name string, set func(backups *sqladmin.BackupConfiguration, value V)) []surface.Path[sqladmin.DatabaseInstance] {
	return setting("backupConfiguration."+name, func(settings *sqladmin.Settings, value V) { set(settings.BackupConfiguration, value) })
}

var instanceSurface = surface.Surface[sqladmin.DatabaseInstance, sqladmin.DatabaseInstance]{
	Name: transformSurface,
	Fields: []instanceField{
		{Name: "machine type", Paths: setting("tier", func(settings *sqladmin.Settings, tier string) { settings.Tier = tier })},
		{Name: "availability", Paths: setting("availabilityType", func(settings *sqladmin.Settings, availability string) { settings.AvailabilityType = availability })},
		{Name: "deletion protection", Paths: setting("deletionProtectionEnabled", func(settings *sqladmin.Settings, protected bool) { settings.DeletionProtectionEnabled = protected })},
		{Name: "daily backups", Paths: backupSetting("enabled", func(backups *sqladmin.BackupConfiguration, enabled bool) { backups.Enabled = enabled })},
		{Name: "point-in-time recovery", Paths: backupSetting("pointInTimeRecoveryEnabled", func(backups *sqladmin.BackupConfiguration, recovers bool) {
			backups.PointInTimeRecoveryEnabled = recovers
		})},
		{Name: "disk type", Paths: setting("diskType", func(settings *sqladmin.Settings, disk string) { settings.DataDiskType = disk })},
		{
			Name:  "edition",
			Paths: setting("edition", func(settings *sqladmin.Settings, edition string) { settings.Edition = edition }),
			Same: func(current, desired *sqladmin.DatabaseInstance) bool {
				return settingsOf(current).Edition == desired.Settings.Edition
			},
			Update: func(update, _, desired *sqladmin.DatabaseInstance) {
				update.Settings.Edition = desired.Settings.Edition
			},
			Show: func(instance *sqladmin.DatabaseInstance) string { return settingsOf(instance).Edition },
		},
		{
			Name:  "disk size",
			Paths: setting("diskSize", func(settings *sqladmin.Settings, size int64) { settings.DataDiskSizeGb = size }),
			Same: func(current, desired *sqladmin.DatabaseInstance) bool {
				held, wanted := settingsOf(current).DataDiskSizeGb, desired.Settings.DataDiskSizeGb
				return held == wanted || held > wanted && isAutoResized(desired)
			},
			Lowers: func(current, desired *sqladmin.DatabaseInstance) bool {
				return settingsOf(current).DataDiskSizeGb > desired.Settings.DataDiskSizeGb
			},
			Update: func(update, _, desired *sqladmin.DatabaseInstance) {
				update.Settings.DataDiskSizeGb = desired.Settings.DataDiskSizeGb
			},
			Show: func(instance *sqladmin.DatabaseInstance) string {
				return strconv.FormatInt(settingsOf(instance).DataDiskSizeGb, 10) + " GB"
			},
		},
		{
			Name: "disk auto-resize",
			Paths: setting("diskAutoresize", func(settings *sqladmin.Settings, resizes bool) {
				settings.StorageAutoResize = &resizes
			}),
			Same: func(current, desired *sqladmin.DatabaseInstance) bool {
				return isAutoResized(current) == isAutoResized(desired)
			},
			Update: func(update, _, desired *sqladmin.DatabaseInstance) {
				resizes := isAutoResized(desired)
				update.Settings.StorageAutoResize = &resizes
			},
			Show: func(instance *sqladmin.DatabaseInstance) string { return surface.ShowOnOff(isAutoResized(instance)) },
		},
		{
			Name:  "disk auto-resize limit",
			Paths: setting("diskAutoresizeLimit", func(settings *sqladmin.Settings, limit int64) { settings.StorageAutoResizeLimit = limit }),
			Same: func(current, desired *sqladmin.DatabaseInstance) bool {
				return settingsOf(current).StorageAutoResizeLimit == desired.Settings.StorageAutoResizeLimit
			},
			Update: func(update, _, desired *sqladmin.DatabaseInstance) {
				update.Settings.StorageAutoResizeLimit = desired.Settings.StorageAutoResizeLimit
				update.Settings.ForceSendFields = append(update.Settings.ForceSendFields, "StorageAutoResizeLimit")
			},
			Show: func(instance *sqladmin.DatabaseInstance) string {
				if limit := settingsOf(instance).StorageAutoResizeLimit; limit > 0 {
					return strconv.FormatInt(limit, 10) + " GB"
				}
				return "no limit"
			},
		},
		{
			Name: "database flag list",
			Paths: setting("databaseFlags", func(settings *sqladmin.Settings, flags []pulumiDatabaseFlag) {
				for _, flag := range flags {
					settings.DatabaseFlags = append(settings.DatabaseFlags, &sqladmin.DatabaseFlags{Name: flag.Name, Value: flag.Value})
				}
			}),
			Same: func(current, desired *sqladmin.DatabaseInstance) bool {
				return slices.Equal(listFlags(settingsOf(current)), listFlags(desired.Settings))
			},
			Update: func(update, _, desired *sqladmin.DatabaseInstance) {
				update.Settings.DatabaseFlags = desired.Settings.DatabaseFlags
				update.Settings.ForceSendFields = append(update.Settings.ForceSendFields, "DatabaseFlags")
			},
			Show: func(instance *sqladmin.DatabaseInstance) string { return orNone(listFlags(settingsOf(instance))) },
		},
		{
			Name: "maintenance window",
			Paths: setting("maintenanceWindow", func(settings *sqladmin.Settings, window pulumiMaintenanceWindow) {
				settings.MaintenanceWindow = &sqladmin.MaintenanceWindow{Day: window.Day, Hour: window.Hour, UpdateTrack: window.UpdateTrack}
			}),
			Same: func(current, desired *sqladmin.DatabaseInstance) bool {
				held, wanted := maintenanceOf(current), maintenanceOf(desired)
				return held.Day == wanted.Day && (wanted.Day == 0 || held.Hour == wanted.Hour) &&
					(wanted.UpdateTrack == "" || held.UpdateTrack == wanted.UpdateTrack)
			},
			Update: func(update, _, desired *sqladmin.DatabaseInstance) {
				window := maintenanceOf(desired)
				update.Settings.MaintenanceWindow = &sqladmin.MaintenanceWindow{
					Day: window.Day, Hour: window.Hour, UpdateTrack: window.UpdateTrack, ForceSendFields: []string{"Day", "Hour"},
				}
			},
			Show: func(instance *sqladmin.DatabaseInstance) string {
				window := maintenanceOf(instance)
				if window.Day == 0 {
					return "any window"
				}
				return fmt.Sprintf("day %d at %02d:00", window.Day, window.Hour)
			},
		},
		{
			Name: "query insights",
			Paths: setting("insightsConfig", func(settings *sqladmin.Settings, insights pulumiInsightsConfig) {
				settings.InsightsConfig = &sqladmin.InsightsConfig{
					QueryInsightsEnabled: insights.QueryInsightsEnabled, QueryPlansPerMinute: insights.QueryPlansPerMinute, QueryStringLength: insights.QueryStringLength,
					RecordApplicationTags: insights.RecordApplicationTags, RecordClientAddress: insights.RecordClientAddress,
				}
			}),
			Same: func(current, desired *sqladmin.DatabaseInstance) bool {
				held, wanted := insightsOf(current), insightsOf(desired)
				if !wanted.QueryInsightsEnabled {
					return !held.QueryInsightsEnabled
				}
				return held.QueryInsightsEnabled && held.RecordApplicationTags == wanted.RecordApplicationTags && held.RecordClientAddress == wanted.RecordClientAddress &&
					(wanted.QueryPlansPerMinute == 0 || held.QueryPlansPerMinute == wanted.QueryPlansPerMinute) &&
					(wanted.QueryStringLength == 0 || held.QueryStringLength == wanted.QueryStringLength)
			},
			Update: func(update, _, desired *sqladmin.DatabaseInstance) {
				insights := insightsOf(desired)
				insights.ForceSendFields = []string{"QueryInsightsEnabled", "RecordApplicationTags", "RecordClientAddress"}
				update.Settings.InsightsConfig = &insights
			},
			Show: func(instance *sqladmin.DatabaseInstance) string {
				return surface.ShowOnOff(insightsOf(instance).QueryInsightsEnabled)
			},
		},
		{
			Name: "data cache",
			Paths: setting("dataCacheConfig.dataCacheEnabled", func(settings *sqladmin.Settings, cached bool) {
				settings.DataCacheConfig = &sqladmin.DataCacheConfig{DataCacheEnabled: cached}
			}),
			Same: func(current, desired *sqladmin.DatabaseInstance) bool {
				return hasDataCache(current) == hasDataCache(desired)
			},
			Update: func(update, _, desired *sqladmin.DatabaseInstance) {
				update.Settings.DataCacheConfig = &sqladmin.DataCacheConfig{DataCacheEnabled: hasDataCache(desired), ForceSendFields: []string{"DataCacheEnabled"}}
			},
			Show: func(instance *sqladmin.DatabaseInstance) string { return surface.ShowOnOff(hasDataCache(instance)) },
		},
		{
			Name:  "backup start time",
			Paths: backupSetting("startTime", func(backups *sqladmin.BackupConfiguration, start string) { backups.StartTime = start }),
			Same: func(current, desired *sqladmin.DatabaseInstance) bool {
				wanted := desired.Settings.BackupConfiguration.StartTime
				return wanted == "" || backupsOf(current).StartTime == wanted
			},
			Update: func(update, _, desired *sqladmin.DatabaseInstance) {
				backupsUpdate(update, desired).StartTime = desired.Settings.BackupConfiguration.StartTime
			},
			Show: func(instance *sqladmin.DatabaseInstance) string { return backupsOf(instance).StartTime },
		},
		{
			Name: "backup retention",
			Paths: backupSetting("backupRetentionSettings", func(backups *sqladmin.BackupConfiguration, retention pulumiRetentionSettings) {
				backups.BackupRetentionSettings = &sqladmin.BackupRetentionSettings{RetainedBackups: retention.RetainedBackups, RetentionUnit: retention.RetentionUnit}
			}),
			Same: func(current, desired *sqladmin.DatabaseInstance) bool {
				return retentionOf(current) == retentionOf(desired)
			},
			Update: func(update, _, desired *sqladmin.DatabaseInstance) {
				retention := retentionOf(desired)
				backupsUpdate(update, desired).BackupRetentionSettings = &sqladmin.BackupRetentionSettings{RetainedBackups: retention.RetainedBackups, RetentionUnit: retention.RetentionUnit}
			},
			Show: func(instance *sqladmin.DatabaseInstance) string {
				retention := retentionOf(instance)
				return fmt.Sprintf("%d by %s", retention.RetainedBackups, strings.ToLower(retention.RetentionUnit))
			},
		},
		{
			Name: "transaction log retention",
			Paths: backupSetting("transactionLogRetentionDays", func(backups *sqladmin.BackupConfiguration, days int64) {
				backups.TransactionLogRetentionDays = days
			}),
			Same: func(current, desired *sqladmin.DatabaseInstance) bool {
				return logRetentionOf(current) == logRetentionOf(desired)
			},
			Update: func(update, _, desired *sqladmin.DatabaseInstance) {
				backupsUpdate(update, desired).TransactionLogRetentionDays = logRetentionOf(desired)
			},
			Show: func(instance *sqladmin.DatabaseInstance) string {
				return strconv.FormatInt(logRetentionOf(instance), 10) + " days"
			},
		},
		{
			Name: "endpoint network",
			Paths: []surface.Path[sqladmin.DatabaseInstance]{surface.NewPath(surface.DatabasePlacement, func(desired *sqladmin.DatabaseInstance, configs []pulumiPSCConfig) error {
				if len(configs) != 1 || len(configs[0].PscAutoConnections) != 1 {
					return fmt.Errorf("an instance takes one pscConfigs entry with one pscAutoConnections entry, the network its endpoint is made in")
				}
				psc := desired.Settings.IpConfiguration.PscConfig
				project := psc.PscAutoConnections[0].ConsumerProject
				network, err := gcloud.PlacedNetworkPath(project, configs[0].PscAutoConnections[0].ConsumerNetwork)
				if err != nil {
					return err
				}
				psc.PscAutoConnections = []*sqladmin.PscAutoConnectionConfig{{ConsumerNetwork: network, ConsumerProject: project}}
				return nil
			})},
		},
	},
	Labels: &surface.Labels[sqladmin.DatabaseInstance, sqladmin.DatabaseInstance]{
		Of: func(instance *sqladmin.DatabaseInstance) *map[string]string { return &instance.Settings.UserLabels },
		Update: func(update *sqladmin.DatabaseInstance, set map[string]string, removed []string) {
			update.Settings.UserLabels = maps.Clone(set)
			if update.Settings.UserLabels == nil {
				update.Settings.UserLabels = map[string]string{}
			}
			update.Settings.ForceSendFields = append(update.Settings.ForceSendFields, "UserLabels")
			for _, key := range removed {
				update.Settings.NullFields = append(update.Settings.NullFields, "UserLabels."+key)
			}
		},
	},
}

func settingsOf(instance *sqladmin.DatabaseInstance) *sqladmin.Settings {
	if instance.Settings == nil {
		return &sqladmin.Settings{}
	}
	return instance.Settings
}

func backupsOf(instance *sqladmin.DatabaseInstance) *sqladmin.BackupConfiguration {
	if backups := settingsOf(instance).BackupConfiguration; backups != nil {
		return backups
	}
	return &sqladmin.BackupConfiguration{}
}

func backupsUpdate(update, desired *sqladmin.DatabaseInstance) *sqladmin.BackupConfiguration {
	if update.Settings.BackupConfiguration == nil {
		update.Settings.BackupConfiguration = &sqladmin.BackupConfiguration{
			Enabled:         backupsOf(desired).Enabled,
			ForceSendFields: []string{"Enabled"},
		}
	}
	return update.Settings.BackupConfiguration
}

func isAutoResized(instance *sqladmin.DatabaseInstance) bool {
	resizes := settingsOf(instance).StorageAutoResize
	return resizes != nil && *resizes
}

func listFlags(settings *sqladmin.Settings) []string {
	flags := make([]string, 0, len(settings.DatabaseFlags))
	for _, flag := range settings.DatabaseFlags {
		flags = append(flags, flag.Name+"="+flag.Value)
	}
	slices.Sort(flags)
	return flags
}

func maintenanceOf(instance *sqladmin.DatabaseInstance) sqladmin.MaintenanceWindow {
	if window := settingsOf(instance).MaintenanceWindow; window != nil {
		return sqladmin.MaintenanceWindow{Day: window.Day, Hour: window.Hour, UpdateTrack: window.UpdateTrack}
	}
	return sqladmin.MaintenanceWindow{}
}

func insightsOf(instance *sqladmin.DatabaseInstance) sqladmin.InsightsConfig {
	if insights := settingsOf(instance).InsightsConfig; insights != nil {
		return sqladmin.InsightsConfig{
			QueryInsightsEnabled: insights.QueryInsightsEnabled, QueryPlansPerMinute: insights.QueryPlansPerMinute, QueryStringLength: insights.QueryStringLength,
			RecordApplicationTags: insights.RecordApplicationTags, RecordClientAddress: insights.RecordClientAddress,
		}
	}
	return sqladmin.InsightsConfig{}
}

func hasDataCache(instance *sqladmin.DatabaseInstance) bool {
	cache := settingsOf(instance).DataCacheConfig
	return cache != nil && cache.DataCacheEnabled
}

type retention struct {
	RetainedBackups int64
	RetentionUnit   string
}

func retentionOf(instance *sqladmin.DatabaseInstance) retention {
	held := retention{RetainedBackups: defaultRetainedBackups, RetentionUnit: defaultRetentionUnit}
	if settings := backupsOf(instance).BackupRetentionSettings; settings != nil {
		if settings.RetainedBackups > 0 {
			held.RetainedBackups = settings.RetainedBackups
		}
		if settings.RetentionUnit != "" {
			held.RetentionUnit = settings.RetentionUnit
		}
	}
	return held
}

func logRetentionOf(instance *sqladmin.DatabaseInstance) int64 {
	if days := backupsOf(instance).TransactionLogRetentionDays; days > 0 {
		return days
	}
	if settingsOf(instance).Edition == cloudSQLEnterprisePlus {
		return defaultPlusLogRetentionDays
	}
	return defaultLogRetentionDays
}

func orNone(listed []string) string {
	if len(listed) == 0 {
		return "none"
	}
	return strings.Join(listed, ", ")
}

func placedNetworkOf(instance *sqladmin.DatabaseInstance) string {
	networks := gcloud.DatabaseNetworks(instance)
	if len(networks) == 0 {
		return ""
	}
	return networks[0]
}

func networkName(network string) string { return path.Base(network) }

func transformTarget() *component.Transform {
	return &component.Transform{Kind: transformKind, Allowed: surface.Allowed{transformSurface: instanceSurface.Paths()}}
}
