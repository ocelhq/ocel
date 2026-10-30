package containers

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/acm"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lb"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/alb"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	lambdaPrincipal       = "lambda.amazonaws.com"
	balancerPrincipal     = "elasticloadbalancing.amazonaws.com"
	livenessRulePriority  = 1
	publicTLSPolicy       = "ELBSecurityPolicy-TLS13-1-2-2021-06"
	defaultCertificateAge = 10 * 365 * 24 * time.Hour
)

func renderLivenessSource() string {
	return fmt.Sprintf(`export const handler = async () => ({ statusCode: 204, statusDescription: "204 No Content", headers: { %q: %q }, body: "" });
`, router.HeaderRouter, alb.Kind)
}

func (w *work) createPublicSecurityGroup(ctx *pulumi.Context, vpc string, tags pulumi.StringMap) (*ec2.SecurityGroup, error) {
	return ec2.NewSecurityGroup(ctx, publicGroupName(w.namespace, w.tier), &ec2.SecurityGroupArgs{
		Name:        pulumi.String(publicGroupName(w.namespace, w.tier)),
		Description: pulumi.String("Ocel: the load balancer the edge forwards the hostnames of the " + string(w.tier) + " class to"),
		VpcId:       pulumi.String(vpc),
		Ingress: ec2.SecurityGroupIngressArray{
			&ec2.SecurityGroupIngressArgs{
				Protocol:    pulumi.String("tcp"),
				FromPort:    pulumi.Int(awsports.PublicListenerPort),
				ToPort:      pulumi.Int(awsports.PublicListenerPort),
				CidrBlocks:  pulumi.ToStringArray(w.fronts.Public),
				Description: pulumi.String("Ocel: only the edge reaches the public front, presenting its client certificate"),
			},
			&ec2.SecurityGroupIngressArgs{
				Protocol:    pulumi.String("tcp"),
				FromPort:    pulumi.Int(originListenerPort),
				ToPort:      pulumi.Int(originListenerPort),
				CidrBlocks:  pulumi.ToStringArray(w.fronts.Public),
				Description: pulumi.String("Ocel: plain http from the edge is redirected to https, forwarding nothing"),
			},
		},
		Egress: ec2.SecurityGroupEgressArray{&ec2.SecurityGroupEgressArgs{
			Protocol: pulumi.String("-1"), FromPort: pulumi.Int(0), ToPort: pulumi.Int(0),
			CidrBlocks: pulumi.StringArray{pulumi.String("0.0.0.0/0")},
		}},
		Tags: namedTags(tags, publicGroupName(w.namespace, w.tier)),
	})
}

func (w *work) runPublicFront(ctx *pulumi.Context, public *ec2.SecurityGroup, subnets []string, tags pulumi.StringMap) error {
	name := awsports.PublicBalancerName(w.namespace, w.tier)
	certificate, key, err := mintDefaultCertificate(name)
	if err != nil {
		return err
	}
	fallback, err := acm.NewCertificate(ctx, component.NameOf(name).Part(certificateWord), &acm.CertificateArgs{
		CertificateBody: pulumi.String(certificate),
		PrivateKey:      pulumi.ToSecret(pulumi.String(key)).(pulumi.StringOutput),
		Tags:            tags,
	}, pulumi.IgnoreChanges([]string{"certificateBody", "privateKey"}))
	if err != nil {
		return err
	}
	balancer, err := lb.NewLoadBalancer(ctx, name, &lb.LoadBalancerArgs{
		Name:             pulumi.String(name),
		LoadBalancerType: pulumi.String("application"),
		Internal:         pulumi.Bool(false),
		SecurityGroups:   pulumi.StringArray{public.ID()},
		Subnets:          pulumi.ToStringArray(subnets),
		Tags:             tags,
	})
	if err != nil {
		return err
	}
	listener, err := lb.NewListener(ctx, component.NameOf(name).Part(httpsWord), &lb.ListenerArgs{
		LoadBalancerArn: balancer.Arn,
		Port:            pulumi.Int(awsports.PublicListenerPort),
		Protocol:        pulumi.String("HTTPS"),
		SslPolicy:       pulumi.String(publicTLSPolicy),
		CertificateArn:  fallback.Arn,
		DefaultActions: lb.ListenerDefaultActionArray{&lb.ListenerDefaultActionArgs{
			Type: pulumi.String("fixed-response"),
			FixedResponse: &lb.ListenerDefaultActionFixedResponseArgs{
				ContentType: pulumi.String(listenerDeniedContentType),
				StatusCode:  pulumi.String(listenerDeniedStatus),
				MessageBody: pulumi.String(listenerDeniedBody),
			},
		}},
		Tags: tags,
	}, pulumi.IgnoreChanges([]string{"mutualAuthentication"}))
	if err != nil {
		return err
	}
	redirect, err := lb.NewListener(ctx, component.NameOf(name).Part(httpWord), &lb.ListenerArgs{
		LoadBalancerArn: balancer.Arn,
		Port:            pulumi.Int(originListenerPort),
		Protocol:        pulumi.String("HTTP"),
		DefaultActions: lb.ListenerDefaultActionArray{&lb.ListenerDefaultActionArgs{
			Type: pulumi.String("redirect"),
			Redirect: &lb.ListenerDefaultActionRedirectArgs{
				Protocol:   pulumi.String("HTTPS"),
				Port:       pulumi.String(strconv.Itoa(awsports.PublicListenerPort)),
				StatusCode: pulumi.String("HTTP_301"),
			},
		}},
		Tags: tags,
	})
	if err != nil {
		return err
	}
	if err := w.runLiveness(ctx, name, listener, tags); err != nil {
		return err
	}
	ctx.Export(outputKeyPublicListener, listener.Arn)
	ctx.Export(outputKeyPublicHost, balancer.DnsName)
	ctx.Export(outputKeyPublicRedirectListener, redirect.Arn)
	return nil
}

func (w *work) runLiveness(ctx *pulumi.Context, balancer string, listener *lb.Listener, tags pulumi.StringMap) error {
	liveness, targets := awsports.PublicLivenessName(w.namespace, w.tier), awsports.PublicTargetGroupName(w.namespace, w.tier)
	role, _, err := component.NewAppRole(ctx, component.AppRole{
		Name:        component.NameOf(liveness),
		Description: pulumi.String("Ocel: the role the public front's liveness answer runs as in the " + string(w.tier) + " tier"),
		Principal:   lambdaPrincipal,
		Boundary:    w.boundary,
		Path:        w.rolePath,
		Tags:        w.tags,
	})
	if err != nil {
		return err
	}
	function, err := lambda.NewFunction(ctx, liveness, &lambda.FunctionArgs{
		Name:        pulumi.String(liveness),
		Description: pulumi.String("Ocel: answers the liveness probe on the public load balancer of the " + string(w.tier) + " tier"),
		Runtime:     pulumi.String(awsports.LambdaNodeRuntime),
		Handler:     pulumi.String("index.handler"),
		Role:        role.Arn,
		Code: pulumi.NewAssetArchive(map[string]any{
			"index.mjs": pulumi.NewStringAsset(renderLivenessSource()),
		}),
		Tags: tags,
	})
	if err != nil {
		return err
	}
	group, err := lb.NewTargetGroup(ctx, targets, &lb.TargetGroupArgs{
		Name:       pulumi.String(targets),
		TargetType: pulumi.String("lambda"),
		Tags:       tags,
	})
	if err != nil {
		return err
	}
	invoke, err := lambda.NewPermission(ctx, component.NameOf(liveness).Part(invokeWord), &lambda.PermissionArgs{
		Action:    pulumi.String("lambda:InvokeFunction"),
		Function:  function.Name,
		Principal: pulumi.String(balancerPrincipal),
		SourceArn: group.Arn,
	})
	if err != nil {
		return err
	}
	if _, err := lb.NewTargetGroupAttachment(ctx, component.NameOf(targets).Part(liveness), &lb.TargetGroupAttachmentArgs{
		TargetGroupArn: group.Arn,
		TargetId:       function.Arn,
	}, pulumi.DependsOn([]pulumi.Resource{invoke})); err != nil {
		return err
	}
	rule, err := lb.NewListenerRule(ctx, component.NameOf(component.NameOf(balancer).Part(httpsWord)).Part(livenessWord), &lb.ListenerRuleArgs{
		ListenerArn: listener.Arn,
		Priority:    pulumi.Int(livenessRulePriority),
		Conditions: lb.ListenerRuleConditionArray{&lb.ListenerRuleConditionArgs{
			PathPattern: &lb.ListenerRuleConditionPathPatternArgs{Values: pulumi.StringArray{pulumi.String(edge.LivenessProbePath)}},
		}},
		Actions: lb.ListenerRuleActionArray{&lb.ListenerRuleActionArgs{
			Type:           pulumi.String("forward"),
			TargetGroupArn: group.Arn,
		}},
		Tags: tags,
	})
	if err != nil {
		return err
	}
	ctx.Export(outputKeyLivenessRole, role.Arn)
	ctx.Export(outputKeyLivenessTargets, group.Arn)
	ctx.Export(outputKeyLivenessRule, rule.Arn)
	return nil
}

func nameCertificateHost(balancer string) string {
	return balancer + ".invalid"
}

func mintDefaultCertificate(name string) (certificate, key string, err error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate the public front's default certificate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("draw the public front's default certificate serial: %w", err)
	}
	host := nameCertificateHost(name)
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(defaultCertificateAge),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if err != nil {
		return "", "", fmt.Errorf("sign the public front's default certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(private)
	if err != nil {
		return "", "", fmt.Errorf("encode the public front's default certificate key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})), nil
}
