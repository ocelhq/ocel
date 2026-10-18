package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	runtimeLayerKeyPrefix = "ocel-runtime-layer"
	runtimeLayerLabel     = "function runtime"
	runtimeDigestLen      = 12

	runtimeLayerGroupKind = "layers"
	runtimeLayerType      = "AWS::Lambda::LayerVersion"

	runtimeLayerStep = "Ensuring the runtime every function boots through (Lambda layers)"

	runtimeLayersRecord = "runtime-layers"

	RuntimeLayersPart    = "runtime-layers"
	runtimeLayersSummary = "the runtime every function boots through, one Lambda layer per architecture and Ocel version"
)

type LayerAPI interface {
	ListLayerVersions(ctx context.Context, in *lambda.ListLayerVersionsInput, optFns ...func(*lambda.Options)) (*lambda.ListLayerVersionsOutput, error)
	PublishLayerVersion(ctx context.Context, in *lambda.PublishLayerVersionInput, optFns ...func(*lambda.Options)) (*lambda.PublishLayerVersionOutput, error)
}

type LayerVersionsAPI interface {
	ListLayerVersions(ctx context.Context, in *lambda.ListLayerVersionsInput, optFns ...func(*lambda.Options)) (*lambda.ListLayerVersionsOutput, error)
}

type LayerTeardownAPI interface {
	ListLayerVersions(ctx context.Context, in *lambda.ListLayerVersionsInput, optFns ...func(*lambda.Options)) (*lambda.ListLayerVersionsOutput, error)
	DeleteLayerVersion(ctx context.Context, in *lambda.DeleteLayerVersionInput, optFns ...func(*lambda.Options)) (*lambda.DeleteLayerVersionOutput, error)
}

var runtimeArchTokens = map[string]string{
	arch.X8664: "X8664",
	arch.ARM64: "Arm64",
}

func runtimeArches() []string { return []string{arch.X8664, arch.ARM64} }

func shortRuntimeDigest(digest string) string {
	if len(digest) <= runtimeDigestLen {
		return digest
	}
	return digest[:runtimeDigestLen]
}

func runtimeLayerName(ns Namespace, tier environment.Tier, architecture, digest string) string {
	return runtimeLayerPrefix(ns, tier, runtimeArchTokens[architecture]) + shortRuntimeDigest(digest)
}

func runtimeLayerPrefix(ns Namespace, tier environment.Tier, archToken string) string {
	return runtimeLayerGroupName(ns, tier) + "-" + archToken + "-"
}

func runtimeLayerGroupName(ns Namespace, tier environment.Tier) string {
	return suffixed(tier, string(ns)+"-runtime")
}

func shippedRuntimeLayers() (map[string]payloads.Payload, error) {
	shipped := make(map[string]payloads.Payload, len(runtimeArches()))
	for _, architecture := range runtimeArches() {
		payload, err := payloads.RuntimeLayer(architecture)
		if err != nil {
			return nil, err
		}
		shipped[architecture] = payload
	}
	return shipped, nil
}

type RuntimeLayerRequest struct {
	ArtifactBucket string
	StateTable     string
}

type RecordStore func(stateTable string) records.Store

func DynamoRecords(dynamo awsports.DynamoAPI) RecordStore {
	return func(stateTable string) records.Store {
		return awsports.KeyValues{Dynamo: dynamo, Tables: awsports.Table(stateTable)}
	}
}

type recordedRuntimeLayers struct {
	Layers []string `json:"layers"`
}

func runtimeLayersKey(tier environment.Tier) records.Key {
	return records.Partition{Tier: tier, Root: stackrecords.RootBootstrap}.Key(runtimeLayersRecord)
}

func decodeRecordedRuntimeLayers(entry records.Entry) ([]string, error) {
	if len(entry.Value) == 0 {
		return nil, nil
	}
	var recorded recordedRuntimeLayers
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		return nil, fmt.Errorf("read the runtime layers %s records: %w", entry.Key, err)
	}
	return recorded.Layers, nil
}

func readRecordedRuntimeLayers(ctx context.Context, store records.Store, tier environment.Tier) ([]string, error) {
	entry, err := records.ReadOrEmpty(ctx, store, runtimeLayersKey(tier))
	if err != nil {
		return nil, err
	}
	return decodeRecordedRuntimeLayers(entry)
}

func recordRuntimeLayer(ctx context.Context, store records.Store, tier environment.Tier, name string) error {
	return records.Change(ctx, store, runtimeLayersKey(tier), func(entry records.Entry) ([]byte, bool, error) {
		names, err := decodeRecordedRuntimeLayers(entry)
		if err != nil || slices.Contains(names, name) {
			return nil, false, err
		}
		names = append(names, name)
		slices.Sort(names)
		value, err := json.Marshal(recordedRuntimeLayers{Layers: names})
		return value, true, err
	})
}

func ReadRuntimeLayers(ctx context.Context, layers LayerAPI, ns Namespace, tier environment.Tier) (map[string]string, error) {
	shipped, err := shippedRuntimeLayers()
	if err != nil {
		return nil, err
	}
	return readRuntimeLayers(ctx, layers, ns, tier, shipped)
}

func readRuntimeLayers(ctx context.Context, layers LayerAPI, ns Namespace, tier environment.Tier, shipped map[string]payloads.Payload) (map[string]string, error) {
	published := map[string]string{}
	for _, architecture := range slices.Sorted(maps.Keys(shipped)) {
		arn, err := latestLayerVersion(ctx, layers, runtimeLayerName(ns, tier, architecture, shipped[architecture].SHA256))
		if err != nil {
			return nil, err
		}
		if arn != "" {
			published[architecture] = arn
		}
	}
	return published, nil
}

func ReadRuntimeLayersToPlan(ctx context.Context, layers LayerAPI, ns Namespace, tier environment.Tier) (map[string]string, error) {
	shipped, err := shippedRuntimeLayers()
	if err != nil {
		return nil, err
	}
	return readRuntimeLayersToPlan(ctx, layers, ns, tier, shipped)
}

func readRuntimeLayersToPlan(ctx context.Context, layers LayerAPI, ns Namespace, tier environment.Tier, shipped map[string]payloads.Payload) (map[string]string, error) {
	published, err := readRuntimeLayers(ctx, layers, ns, tier, shipped)
	if err != nil {
		return nil, err
	}
	for architecture := range shipped {
		if published[architecture] == "" {
			published[architecture] = awsports.RuntimeLayerToPublish
		}
	}
	return published, nil
}

func EnsureRuntimeLayers(ctx context.Context, apis APIs, ns Namespace, tier environment.Tier, req RuntimeLayerRequest, runProgress progress.Log) (map[string]string, error) {
	shipped, err := shippedRuntimeLayers()
	if err != nil {
		return nil, err
	}
	return ensureRuntimeLayers(ctx, apis, ns, tier, req, shipped, ensureProgress(runProgress))
}

func ensureRuntimeLayers(ctx context.Context, apis APIs, ns Namespace, tier environment.Tier, req RuntimeLayerRequest, shipped map[string]payloads.Payload, runProgress progress.Log) (map[string]string, error) {
	published := map[string]string{}
	for _, architecture := range slices.Sorted(maps.Keys(shipped)) {
		payload := shipped[architecture]
		name := runtimeLayerName(ns, tier, architecture, payload.SHA256)
		arn, err := latestLayerVersion(ctx, apis.Layers, name)
		if err != nil {
			return nil, err
		}
		if arn == "" {
			if err := recordRuntimeLayer(ctx, apis.Records(req.StateTable), tier, name); err != nil {
				return nil, fmt.Errorf("record Lambda layer %s before publishing it: %w", name, err)
			}
			if arn, err = publishRuntimeLayer(ctx, apis, tier, name, architecture, req.ArtifactBucket, payload); err != nil {
				return nil, err
			}
			runProgress.Say(fmt.Sprintf("Published the %s runtime this Ocel version ships as Lambda layer %s", architecture, arn))
		}
		published[architecture] = arn
	}
	return published, nil
}

func publishRuntimeLayer(ctx context.Context, apis APIs, tier environment.Tier, name, architecture, bucket string, payload payloads.Payload) (string, error) {
	at, err := payloads.Place(ctx, apis.Store, bucket, runtimeLayerKeyPrefix, runtimeLayerLabel+" ("+architecture+")", payload)
	if err != nil {
		return "", err
	}
	out, err := apis.Layers.PublishLayerVersion(ctx, &lambda.PublishLayerVersionInput{
		LayerName:               aws.String(name),
		Description:             aws.String(fmt.Sprintf("Ocel %s runtime (%s) - payload %s", architecture, tier, shortRuntimeDigest(payload.SHA256))),
		Content:                 &lambdatypes.LayerVersionContentInput{S3Bucket: aws.String(at.Bucket), S3Key: aws.String(at.Key)},
		CompatibleArchitectures: []lambdatypes.Architecture{lambdatypes.Architecture(architecture)},
	})
	if err != nil {
		return "", fmt.Errorf("publish the %s runtime this Ocel version ships as Lambda layer %s: %w", architecture, name, err)
	}
	return aws.ToString(out.LayerVersionArn), nil
}

func latestLayerVersion(ctx context.Context, layers LayerAPI, name string) (string, error) {
	out, err := layers.ListLayerVersions(ctx, &lambda.ListLayerVersionsInput{LayerName: aws.String(name)})
	if isLayerNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("list the versions of Lambda layer %s: %w", name, err)
	}
	var latest lambdatypes.LayerVersionsListItem
	for _, version := range out.LayerVersions {
		if version.Version > latest.Version {
			latest = version
		}
	}
	return aws.ToString(latest.LayerVersionArn), nil
}

func isLayerNotFound(err error) bool {
	var missing *lambdatypes.ResourceNotFoundException
	return errors.As(err, &missing)
}

func planRuntimeLayers(ctx context.Context, layers LayerAPI, read Reading) provider.ChangeGroup {
	group := provider.ChangeGroup{Kind: runtimeLayerGroupKind, Name: runtimeLayerGroupName(read.ns, read.tier)}
	shipped, err := shippedRuntimeLayers()
	if err != nil {
		group.Action, group.Reason = provider.ActionKeep, err.Error()
		return group
	}
	published, err := readRuntimeLayers(ctx, layers, read.ns, read.tier, shipped)
	if err != nil {
		group.Action, group.Reason = provider.ActionUpdate, provider.WithDetailUnavailable("")
		return group
	}
	for _, architecture := range slices.Sorted(maps.Keys(shipped)) {
		group.Changes = append(group.Changes, provider.Change{
			Kind:   runtimeLayerType,
			Name:   runtimeLayerName(read.ns, read.tier, architecture, shipped[architecture].SHA256),
			Action: provider.KeepOrCreate(published[architecture] != ""),
		})
	}
	group.Action, group.Reason = provider.RollUp(group.Changes)
	return group
}

type runtimeLayerVersion struct {
	name    string
	version int64
}

func (v runtimeLayerVersion) String() string { return fmt.Sprintf("%s:%d", v.name, v.version) }

func runtimeLayerVersions(ctx context.Context, layers LayerVersionsAPI, store records.Store, tier environment.Tier) ([]runtimeLayerVersion, error) {
	names, err := readRecordedRuntimeLayers(ctx, store, tier)
	if err != nil {
		return nil, err
	}
	var versions []runtimeLayerVersion
	for _, name := range names {
		var marker *string
		for {
			page, err := layers.ListLayerVersions(ctx, &lambda.ListLayerVersionsInput{LayerName: aws.String(name), Marker: marker})
			if isLayerNotFound(err) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("list the versions of Lambda layer %s: %w", name, err)
			}
			for _, version := range page.LayerVersions {
				versions = append(versions, runtimeLayerVersion{name: name, version: version.Version})
			}
			if marker = page.NextMarker; marker == nil {
				break
			}
		}
	}
	return versions, nil
}

func ReadRuntimeLayersPart(ctx context.Context, layers LayerVersionsAPI, store records.Store, tier environment.Tier) (provider.BootstrapPart, bool, error) {
	versions, err := runtimeLayerVersions(ctx, layers, store, tier)
	if err != nil || len(versions) == 0 {
		return provider.BootstrapPart{}, false, err
	}
	resources := make([]provider.PartResource, 0, len(versions))
	for _, version := range versions {
		resources = append(resources, provider.PartResource{Kind: runtimeLayerType, ID: version.String()})
	}
	return provider.BootstrapPart{
		Name:               RuntimeLayersPart,
		Summary:            runtimeLayersSummary,
		Current:            true,
		Resources:          resources,
		UsedByEveryProject: true,
	}, true, nil
}

func removeRuntimeLayers(ctx context.Context, layers LayerTeardownAPI, store records.Store, ns Namespace, tier environment.Tier) (provider.ChangeGroup, bool) {
	group := provider.ChangeGroup{Kind: runtimeLayerGroupKind, Name: runtimeLayerGroupName(ns, tier), Action: provider.ActionDelete}
	versions, err := runtimeLayerVersions(ctx, layers, store, tier)
	if err != nil {
		group.Reason = provider.WithDetailUnavailable("")
		return group, true
	}
	for _, version := range versions {
		group.Changes = append(group.Changes, provider.Change{Kind: runtimeLayerType, Name: version.String(), Action: provider.ActionDelete})
	}
	return group, len(versions) > 0
}

func deleteRuntimeLayers(ctx context.Context, layers LayerTeardownAPI, store records.Store, tier environment.Tier, progress progress.Log) error {
	versions, err := runtimeLayerVersions(ctx, layers, store, tier)
	if err != nil {
		return err
	}
	for _, version := range versions {
		_, err := layers.DeleteLayerVersion(ctx, &lambda.DeleteLayerVersionInput{
			LayerName:     aws.String(version.name),
			VersionNumber: aws.Int64(version.version),
		})
		if err != nil && !isLayerNotFound(err) {
			return fmt.Errorf("delete Lambda layer version %s: %w", version, err)
		}
		progress.Say("Removed Lambda layer version " + version.String())
	}
	return records.Forget(ctx, store, runtimeLayersKey(tier))
}

func shapedRuntimeLayers() []pricing.Shaped {
	shaped := make([]pricing.Shaped, 0, len(runtimeArches()))
	for _, architecture := range runtimeArches() {
		shaped = append(shaped, pricing.Shaped{Name: "RuntimeLayer" + runtimeArchTokens[architecture], Type: shapedTypes[runtimeLayerType].token, Properties: map[string]any{}})
	}
	return shaped
}
