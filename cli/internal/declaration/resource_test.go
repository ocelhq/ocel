package declaration

import (
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestParseKeepsTheTypedConfigAndRefusesAMalformedDeclaration(t *testing.T) {
	t.Parallel()

	rejects := []struct {
		name string
		req  *resourcesv1.DeclareRequest
	}{
		{
			name: "rejects an unspecified resource type",
			req: &resourcesv1.DeclareRequest{
				Resource: &resourcesv1.ResourceIdentifier{Name: "main"},
			},
		},
		{
			name: "rejects a missing resource",
			req:  &resourcesv1.DeclareRequest{},
		},
		{
			name: "rejects a type without its config",
			req: &resourcesv1.DeclareRequest{
				Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
			},
		},
		{
			name: "rejects a worker declared with a topic's config",
			req: &resourcesv1.DeclareRequest{
				Resource: &resourcesv1.ResourceIdentifier{Name: "media", Type: resourcesv1.ResourceType_RESOURCE_TYPE_WORKER},
				Config:   &resourcesv1.DeclareRequest_Topic{Topic: &resourcesv1.TopicConfig{}},
			},
		},
		{
			name: "rejects a consumer declared with no config",
			req: &resourcesv1.DeclareRequest{
				Resource: &resourcesv1.ResourceIdentifier{Name: "email", Type: resourcesv1.ResourceType_RESOURCE_TYPE_CONSUMER},
			},
		},
		{
			name: "rejects a config that contradicts the type",
			req: &resourcesv1.DeclareRequest{
				Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
				Config:   &resourcesv1.DeclareRequest_Bucket{Bucket: &resourcesv1.BucketConfig{}},
			},
		},
	}
	for _, tc := range rejects {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := Parse(tc.req); err == nil {
				t.Fatalf("Parse: expected error, got nil")
			}
		})
	}

	t.Run("returns name and type", func(t *testing.T) {
		t.Parallel()

		res, err := Parse(&resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
			Config:   &resourcesv1.DeclareRequest_Postgres{Postgres: &resourcesv1.PostgresConfig{}},
		})
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if res.Name != "main" {
			t.Fatalf("Name = %q, want %q", res.Name, "main")
		}
		if res.Type != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
			t.Fatalf("Type = %v, want %v", res.Type, resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES)
		}
	})

	t.Run("parses typed postgres config", func(t *testing.T) {
		t.Parallel()

		res, err := Parse(&resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
			Config:   &resourcesv1.DeclareRequest_Postgres{Postgres: &resourcesv1.PostgresConfig{Version: "17"}},
		})
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if res.Postgres == nil || res.Postgres.Version != "17" {
			t.Fatalf("Postgres = %+v, want version 17", res.Postgres)
		}
	})

	t.Run("parses typed bucket config", func(t *testing.T) {
		t.Parallel()

		res, err := Parse(&resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "storage", Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET},
			Config:   &resourcesv1.DeclareRequest_Bucket{Bucket: &resourcesv1.BucketConfig{AllowedOrigins: []string{"https://app.example.com"}}},
		})
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if res.Bucket == nil || len(res.Bucket.GetAllowedOrigins()) != 1 || res.Bucket.GetAllowedOrigins()[0] != "https://app.example.com" {
			t.Fatalf("Bucket = %+v, want allowed_origins [https://app.example.com]", res.Bucket)
		}
	})

	t.Run("bucket config leaves postgres nil", func(t *testing.T) {
		t.Parallel()

		res, err := Parse(&resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "storage", Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET},
			Config:   &resourcesv1.DeclareRequest_Bucket{Bucket: &resourcesv1.BucketConfig{}},
		})
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if res.Postgres != nil {
			t.Fatalf("Postgres = %+v, want nil", res.Postgres)
		}
	})
}

func TestParseRecordsTopicsTasksConsumersAndWorkersWithTheirConfig(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		req   *resourcesv1.DeclareRequest
		check func(Resource) bool
	}{
		{
			name: "a topic",
			req: &resourcesv1.DeclareRequest{
				Resource: &resourcesv1.ResourceIdentifier{Name: "orders", Type: resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC},
				Config:   &resourcesv1.DeclareRequest_Topic{Topic: &resourcesv1.TopicConfig{Ordered: true}},
				Source:   "src/orders.ts:4",
			},
			check: func(r Resource) bool { return r.Topic.GetOrdered() && r.Source == "src/orders.ts:4" },
		},
		{
			name: "a task",
			req: &resourcesv1.DeclareRequest{
				Resource: &resourcesv1.ResourceIdentifier{Name: "resize-image", Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK},
				Config:   &resourcesv1.DeclareRequest_Task{Task: &resourcesv1.TaskConfig{Worker: "media", Cron: "0 * * * *"}},
			},
			check: func(r Resource) bool { return r.Task.GetWorker() == "media" && r.Task.GetCron() == "0 * * * *" },
		},
		{
			name: "a consumer",
			req: &resourcesv1.DeclareRequest{
				Resource: &resourcesv1.ResourceIdentifier{Name: "email", Type: resourcesv1.ResourceType_RESOURCE_TYPE_CONSUMER},
				Config:   &resourcesv1.DeclareRequest_Consumer{Consumer: &resourcesv1.ConsumerConfig{Topic: "orders", Concurrency: 5}},
			},
			check: func(r Resource) bool { return r.Consumer.GetTopic() == "orders" && r.Consumer.GetConcurrency() == 5 },
		},
		{
			name: "a worker",
			req: &resourcesv1.DeclareRequest{
				Resource: &resourcesv1.ResourceIdentifier{Name: "media", Type: resourcesv1.ResourceType_RESOURCE_TYPE_WORKER},
				Config:   &resourcesv1.DeclareRequest_Worker{Worker: &resourcesv1.WorkerConfig{Concurrency: 8}},
			},
			check: func(r Resource) bool { return r.Worker.GetConcurrency() == 8 },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res, err := Parse(tc.req)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if res.Name != tc.req.GetResource().GetName() || res.Type != tc.req.GetResource().GetType() || !tc.check(res) {
				t.Errorf("Parse = %+v, want the declaration's name, type and config kept", res)
			}
		})
	}
}

func TestParseRecordsAKVStoreAndItsEntries(t *testing.T) {
	t.Parallel()

	res, err := Parse(&resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Name: "cache", Type: resourcesv1.ResourceType_RESOURCE_TYPE_KV},
		Config: &resourcesv1.DeclareRequest_Kv{Kv: &resourcesv1.KvConfig{
			Eviction: "allkeys-lru",
			Memory:   "256mb",
			Entries: []*resourcesv1.KvEntry{
				{Name: "requests", Pattern: "requests/:userId", Shape: resourcesv1.KvShape_KV_SHAPE_COUNTER, Source: "src/cache.ts:4"},
				{Name: "session", Pattern: "session/:id", Shape: resourcesv1.KvShape_KV_SHAPE_JSON, Source: "src/cache.ts:5"},
			},
		}},
		Source: "src/cache.ts:2",
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.Type != resourcesv1.ResourceType_RESOURCE_TYPE_KV || res.Name != "cache" || res.Source != "src/cache.ts:2" {
		t.Errorf("Parse = %+v, want kv store cache declared at src/cache.ts:2", res)
	}
	entries := res.KV.GetEntries()
	if res.KV.GetEviction() != "allkeys-lru" || len(entries) != 2 || entries[1].GetPattern() != "session/:id" || entries[1].GetSource() != "src/cache.ts:5" {
		t.Errorf("Parse KV = %v, want the store's eviction and both entries kept with their patterns and lines", res.KV)
	}
}

func TestParseRecordsARealtimeResourceAndItsChannels(t *testing.T) {
	t.Parallel()

	res, err := Parse(&resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Name: "app", Type: resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME},
		Config: &resourcesv1.DeclareRequest_Realtime{Realtime: &resourcesv1.RealtimeConfig{
			Channels: []*resourcesv1.RealtimeChannel{
				{Pattern: "orders/:orderId", Subscribe: resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_RULE, Publish: resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER, Source: "src/realtime.ts:8"},
				{Pattern: "projects/:projectId/deploys/:deployId", Wildcard: true, Schema: `{"type":"object"}`, Subscribe: resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_RULE, Publish: resourcesv1.RealtimePublish_REALTIME_PUBLISH_RULE, Source: "src/realtime.ts:14"},
				{Pattern: "status", Subscribe: resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_PUBLIC, Publish: resourcesv1.RealtimePublish_REALTIME_PUBLISH_SERVER, Source: "src/realtime.ts:20"},
			},
		}},
		Source: "src/realtime.ts:3",
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.Type != resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME || res.Name != "app" || res.Source != "src/realtime.ts:3" {
		t.Errorf("Parse = %+v, want realtime app declared at src/realtime.ts:3", res)
	}
	channels := res.Realtime.GetChannels()
	if len(channels) != 3 || !channels[1].GetWildcard() || channels[1].GetSource() != "src/realtime.ts:14" || channels[2].GetSubscribe() != resourcesv1.RealtimeSubscribe_REALTIME_SUBSCRIBE_PUBLIC {
		t.Errorf("Parse Realtime = %v, want all three channels kept with their modes and lines", res.Realtime)
	}
}
