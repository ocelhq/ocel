package sdkconfig

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"

	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/tracing"
)

const scope = "github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"

const (
	dynamoTableNames = "aws.dynamodb.table_names"
	dynamoKeyPrefix  = "aws.dynamodb.key."
)

var s3Fields = map[string]string{
	"Bucket": "aws.s3.bucket",
	"Key":    "aws.s3.key",
	"Prefix": "aws.s3.prefix",
}

func addOperationSpan(stack *middleware.Stack) error {
	return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("OcelOperationSpan", traceOperation), middleware.Before)
}

func traceOperation(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (out middleware.InitializeOutput, metadata middleware.Metadata, err error) {
	service, operation := middleware.GetServiceID(ctx), middleware.GetOperationName(ctx)
	name, attrs := tracing.NameRPCSpan("aws-api", service, operation)
	ctx, span := otel.Tracer(scope).Start(ctx, name, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	defer func() { tracing.EndSpan(span, err) }()
	switch service {
	case dynamodb.ServiceID:
		span.SetAttributes(dynamoAttributes(in.Parameters)...)
	case s3.ServiceID:
		span.SetAttributes(stringFieldAttributes(in.Parameters, s3Fields)...)
	}

	out, metadata, err = next.HandleInitialize(ctx, in)
	if id, ok := awsmiddleware.GetRequestIDMetadata(metadata); ok {
		span.SetAttributes(attribute.String("aws.request_id", id))
	}
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			span.SetAttributes(attribute.String("error.type", apiErr.ErrorCode()))
		}
	}
	return out, metadata, err
}

func dynamoAttributes(params any) []attribute.KeyValue {
	switch in := params.(type) {
	case *dynamodb.TransactWriteItemsInput:
		return []attribute.KeyValue{attribute.StringSlice(dynamoTableNames, transactionTables(in.TransactItems))}
	case *dynamodb.BatchWriteItemInput:
		return []attribute.KeyValue{attribute.StringSlice(dynamoTableNames, slices.Sorted(maps.Keys(in.RequestItems)))}
	case *dynamodb.BatchGetItemInput:
		return []attribute.KeyValue{attribute.StringSlice(dynamoTableNames, slices.Sorted(maps.Keys(in.RequestItems)))}
	}
	fields := structOf(params)
	if !fields.IsValid() {
		return nil
	}
	var attrs []attribute.KeyValue
	if table, ok := stringField(fields, "TableName"); ok {
		attrs = append(attrs, attribute.StringSlice(dynamoTableNames, []string{table}))
	}
	if index, ok := stringField(fields, "IndexName"); ok {
		attrs = append(attrs, attribute.String("aws.dynamodb.index_name", index))
	}
	if key, ok := fieldValue(fields, "Key").(map[string]ddbtypes.AttributeValue); ok {
		for _, name := range slices.Sorted(maps.Keys(key)) {
			attrs = append(attrs, attribute.String(dynamoKeyPrefix+name, keyValue(key[name])))
		}
	}
	return attrs
}

func transactionTables(items []ddbtypes.TransactWriteItem) []string {
	var tables []string
	for _, item := range items {
		var table *string
		switch {
		case item.Put != nil:
			table = item.Put.TableName
		case item.Update != nil:
			table = item.Update.TableName
		case item.Delete != nil:
			table = item.Delete.TableName
		case item.ConditionCheck != nil:
			table = item.ConditionCheck.TableName
		}
		if table != nil && !slices.Contains(tables, *table) {
			tables = append(tables, *table)
		}
	}
	return tables
}

func keyValue(v ddbtypes.AttributeValue) string {
	switch v := v.(type) {
	case *ddbtypes.AttributeValueMemberS:
		return v.Value
	case *ddbtypes.AttributeValueMemberN:
		return v.Value
	default:
		return reflect.TypeOf(v).Elem().Name()
	}
}

func stringFieldAttributes(params any, fields map[string]string) []attribute.KeyValue {
	value := structOf(params)
	if !value.IsValid() {
		return nil
	}
	var attrs []attribute.KeyValue
	for _, field := range slices.Sorted(maps.Keys(fields)) {
		if s, ok := stringField(value, field); ok {
			attrs = append(attrs, attribute.String(fields[field], s))
		}
	}
	return attrs
}

func structOf(params any) reflect.Value {
	value := reflect.Indirect(reflect.ValueOf(params))
	if value.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	return value
}

func fieldValue(value reflect.Value, name string) any {
	field := value.FieldByName(name)
	if !field.IsValid() || !field.CanInterface() {
		return nil
	}
	return field.Interface()
}

func stringField(value reflect.Value, name string) (string, bool) {
	s, ok := fieldValue(value, name).(*string)
	if !ok || s == nil {
		return "", false
	}
	return *s, true
}
