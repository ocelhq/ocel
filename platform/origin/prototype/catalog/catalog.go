package catalog

import (
	origin "github.com/ocelhq/ocel/platform/origin/contract"
	"github.com/ocelhq/ocel/platform/origin/prototype/fake"
)

const (
	AWS        origin.Kind = "aws"
	Cloudflare origin.Kind = "cloudflare"
	VPS        origin.Kind = "vps"
	GCP        origin.Kind = "gcp"

	CloudFront     origin.Kind = "cloudfront"
	APIGateway     origin.Kind = "api-gateway"
	CloudflareEdge origin.Kind = "cloudflare"
	Caddy          origin.Kind = "caddy"
	CloudCDN       origin.Kind = "cloud-cdn"

	Route53       origin.Kind = "route53"
	CloudflareDNS origin.Kind = "cloudflare"
	CloudDNS      origin.Kind = "cloud-dns"

	S3       origin.Kind = "s3"
	R2       origin.Kind = "r2"
	MinIO    origin.Kind = "minio"
	GCS      origin.Kind = "gcs"
	S3Keys   origin.Kind = "s3-keys"
	GCSKeys  origin.Kind = "gcs-hmac"
	Aurora   origin.Kind = "aurora"
	Docker   origin.Kind = "postgres-docker"
	CloudSQL origin.Kind = "cloud-sql"
	Neon     origin.Kind = "neon"
)

func backing(role origin.Role, kind, native origin.Kind, protocol origin.Protocol, brings ...string) origin.Backing {
	return fake.NewBacking(origin.BackingFacts{Role: role, Kind: kind, Native: native, Protocol: protocol, Brings: brings})
}

func Independent() []origin.Backing {
	return []origin.Backing{
		backing(origin.RoleEdge, CloudflareEdge, "", "", "CF_API_TOKEN"),
		backing(origin.RoleDNS, CloudflareDNS, "", "", "CF_API_TOKEN"),
		backing(origin.RolePostgres, Neon, "", origin.ProtocolPostgres, "DATABASE_URL"),
		backing(origin.RoleBucket, S3Keys, "", origin.ProtocolS3, "ACCESS_KEY_ID", "SECRET_ACCESS_KEY"),
		backing(origin.RoleBucket, GCSKeys, "", origin.ProtocolGCS, "HMAC_KEY", "HMAC_SECRET"),
	}
}

func Origins() map[origin.Kind]origin.Origin {
	return map[origin.Kind]origin.Origin{
		AWS: fake.NewOrigin(origin.Facts{
			Kind:        AWS,
			Defaults:    map[origin.Role]origin.Kind{origin.RoleBucket: S3, origin.RolePostgres: Aurora},
			DefaultEdge: CloudFront,
			Speaks:      []origin.Protocol{origin.ProtocolS3},
			Identity:    "arn:aws:iam::role/ocel",
		},
			backing(origin.RoleEdge, CloudFront, AWS, ""),
			backing(origin.RoleEdge, APIGateway, AWS, ""),
			backing(origin.RoleDNS, Route53, AWS, ""),
			backing(origin.RoleBucket, S3, AWS, origin.ProtocolS3),
			backing(origin.RolePostgres, Aurora, AWS, origin.ProtocolPostgres),
		),
		Cloudflare: fake.NewOrigin(origin.Facts{
			Kind:        Cloudflare,
			Defaults:    map[origin.Role]origin.Kind{origin.RoleBucket: R2},
			DefaultEdge: CloudflareEdge,
			Speaks:      []origin.Protocol{origin.ProtocolR2Bind, origin.ProtocolS3},
			Identity:    "cf:account/worker",
		},
			backing(origin.RoleBucket, R2, Cloudflare, origin.ProtocolR2Bind),
		),
		VPS: fake.NewOrigin(origin.Facts{
			Kind:        VPS,
			Defaults:    map[origin.Role]origin.Kind{origin.RoleBucket: MinIO, origin.RolePostgres: Docker},
			DefaultEdge: Caddy,
			Speaks:      []origin.Protocol{origin.ProtocolS3},
			Identity:    "ssh:ocel@host",
		},
			backing(origin.RoleEdge, Caddy, VPS, ""),
			backing(origin.RoleBucket, MinIO, VPS, origin.ProtocolS3),
			backing(origin.RolePostgres, Docker, VPS, origin.ProtocolPostgres),
		),
		GCP: fake.NewOrigin(origin.Facts{
			Kind:        GCP,
			Defaults:    map[origin.Role]origin.Kind{origin.RoleBucket: GCS, origin.RolePostgres: CloudSQL},
			DefaultEdge: CloudCDN,
			Speaks:      []origin.Protocol{origin.ProtocolGCS, origin.ProtocolS3},
			Identity:    "serviceAccount:ocel@project",
		},
			backing(origin.RoleEdge, CloudCDN, GCP, ""),
			backing(origin.RoleDNS, CloudDNS, GCP, ""),
			backing(origin.RoleBucket, GCS, GCP, origin.ProtocolGCS),
			backing(origin.RolePostgres, CloudSQL, GCP, origin.ProtocolPostgres),
		),
	}
}
