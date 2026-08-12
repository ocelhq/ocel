package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const getParametersLimit = 10

type SSMBatchAPI interface {
	GetParameters(ctx context.Context, in *ssm.GetParametersInput, optFns ...func(*ssm.Options)) (*ssm.GetParametersOutput, error)
}

type ClassParams struct {
	Passphrase string

	EdgeCredentials    EdgeCredentials
	EdgeCredentialsErr error

	EdgeValues    map[string]string
	EdgeValuesErr error

	CacheStore       CacheStore
	DeploymentsStore DeploymentsStore
	ISRWriter        ISRWriter
	ISRWriterSeed    string
	RootStackState   edge.RootStackState
	PreviewDomain    PreviewDomain
}

func ReadClassParams(ctx context.Context, api SSMBatchAPI, class, slug string) (ClassParams, error) {
	names, err := edgeNamesFor(class)
	if err != nil {
		return ClassParams{}, err
	}
	prefix, err := rootStackStateParamPrefixFor(class)
	if err != nil {
		return ClassParams{}, err
	}
	rootStackParam := rootStackStateParamName(prefix, slug)

	wanted := []string{
		PassphraseParamName,
		names.credentialsParam,
		names.valuesParam,
		names.cacheStoreParam,
		names.deploymentsStoreParam,
		names.isrWriterParam,
		names.isrWriterSeedParam,
		rootStackParam,
	}
	if class == ClassPreview {
		wanted = append(wanted, PreviewDomainParamName)
	}

	found, err := getParameters(ctx, api, wanted)
	if err != nil {
		return ClassParams{}, err
	}

	var p ClassParams

	passphrase, ok := found[PassphraseParamName]
	if !ok {
		return ClassParams{}, fmt.Errorf("read passphrase parameter: %s not found", PassphraseParamName)
	}
	p.Passphrase = passphrase

	if raw, ok := found[names.credentialsParam]; ok {
		if err := json.Unmarshal([]byte(raw), &p.EdgeCredentials); err != nil {
			p.EdgeCredentials = EdgeCredentials{}
			p.EdgeCredentialsErr = fmt.Errorf("parse edge credentials: %w", err)
		}
	} else {
		p.EdgeCredentialsErr = fmt.Errorf("read edge credentials parameter: %s not found", names.credentialsParam)
	}

	if raw, ok := found[names.valuesParam]; ok {
		if err := json.Unmarshal([]byte(raw), &p.EdgeValues); err != nil {
			p.EdgeValues = nil
			p.EdgeValuesErr = fmt.Errorf("parse edge values: %w", err)
		}
	}

	if raw, ok := found[names.cacheStoreParam]; ok {
		if err := json.Unmarshal([]byte(raw), &p.CacheStore); err != nil {
			return ClassParams{}, fmt.Errorf("parse cache store: %w", err)
		}
	}
	if raw, ok := found[names.deploymentsStoreParam]; ok {
		if err := json.Unmarshal([]byte(raw), &p.DeploymentsStore); err != nil {
			return ClassParams{}, fmt.Errorf("parse deployments store: %w", err)
		}
	}
	if raw, ok := found[names.isrWriterParam]; ok {
		if err := json.Unmarshal([]byte(raw), &p.ISRWriter); err != nil {
			return ClassParams{}, fmt.Errorf("parse isr writer: %w", err)
		}
	}
	p.ISRWriterSeed = found[names.isrWriterSeedParam]
	if raw, ok := found[PreviewDomainParamName]; ok {
		if err := json.Unmarshal([]byte(raw), &p.PreviewDomain); err != nil {
			return ClassParams{}, fmt.Errorf("parse preview domain: %w", err)
		}
	}
	if raw, ok := found[rootStackParam]; ok {
		if err := json.Unmarshal([]byte(raw), &p.RootStackState); err != nil {
			return ClassParams{}, fmt.Errorf("parse root-stack state: %w", err)
		}
	}
	return p, nil
}

type TeardownParams struct {
	Passphrase    string
	PassphraseErr error

	CacheStore     CacheStore
	ISRWriter      ISRWriter
	RootStackState edge.RootStackState
}

func ReadTeardownParams(ctx context.Context, api SSMBatchAPI, class, slug string) (TeardownParams, error) {
	names, err := edgeNamesFor(class)
	if err != nil {
		return TeardownParams{}, err
	}
	prefix, err := rootStackStateParamPrefixFor(class)
	if err != nil {
		return TeardownParams{}, err
	}
	rootStackParam := rootStackStateParamName(prefix, slug)

	found, err := getParameters(ctx, api, []string{
		PassphraseParamName,
		names.cacheStoreParam,
		names.isrWriterParam,
		rootStackParam,
	})
	if err != nil {
		return TeardownParams{}, err
	}

	var p TeardownParams

	passphrase, ok := found[PassphraseParamName]
	if !ok {
		p.PassphraseErr = fmt.Errorf("read passphrase parameter: %s not found", PassphraseParamName)
	}
	p.Passphrase = passphrase

	if raw, ok := found[names.cacheStoreParam]; ok {
		if err := json.Unmarshal([]byte(raw), &p.CacheStore); err != nil {
			p.CacheStore = CacheStore{}
		}
	}
	if raw, ok := found[names.isrWriterParam]; ok {
		if err := json.Unmarshal([]byte(raw), &p.ISRWriter); err != nil {
			p.ISRWriter = ISRWriter{}
		}
	}
	if raw, ok := found[rootStackParam]; ok {
		if err := json.Unmarshal([]byte(raw), &p.RootStackState); err != nil {
			return TeardownParams{}, fmt.Errorf("parse root-stack state: %w", err)
		}
	}
	return p, nil
}

func getParameters(ctx context.Context, api SSMBatchAPI, names []string) (map[string]string, error) {
	found := make(map[string]string, len(names))
	for start := 0; start < len(names); start += getParametersLimit {
		end := min(start+getParametersLimit, len(names))
		out, err := api.GetParameters(ctx, &ssm.GetParametersInput{
			Names:          names[start:end],
			WithDecryption: aws.Bool(true),
		})
		if err != nil {
			return nil, fmt.Errorf("read bootstrap parameters: %w", err)
		}
		for _, param := range out.Parameters {
			found[aws.ToString(param.Name)] = aws.ToString(param.Value)
		}
	}
	return found, nil
}
