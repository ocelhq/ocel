package cfn

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const FormatVersion = "2010-09-09"

type Template struct {
	Description string
	Parameters  []Parameter
	Resources   []Resource
	Outputs     []Output
}

type Parameter struct {
	Name        string
	Type        string
	Description string
}

type Resource struct {
	ID         string
	Type       string
	Metadata   Metadata
	Properties map[string]any
}

type Metadata struct {
	Description string `json:"Description,omitempty"`
}

type Output struct {
	Name        string
	Description string
	Value       any
}

type Intrinsic struct {
	function string
	argument any
}

func Ref(name string) Intrinsic { return Intrinsic{function: "Ref", argument: name} }

func GetAtt(resource, attribute string) Intrinsic {
	return Intrinsic{function: "Fn::GetAtt", argument: []string{resource, attribute}}
}

func Sub(text string) Intrinsic { return Intrinsic{function: "Fn::Sub", argument: text} }

func (i Intrinsic) MarshalJSON() ([]byte, error) {
	return encodeJSON(map[string]any{i.function: i.argument})
}

func (t Template) Body() (string, error) {
	document := object{{"AWSTemplateFormatVersion", FormatVersion}, {"Description", t.Description}}
	if len(t.Parameters) > 0 {
		parameters := make(object, 0, len(t.Parameters))
		for _, p := range t.Parameters {
			parameters = append(parameters, member{p.Name, object{{"Type", p.Type}, {"Description", p.Description}}})
		}
		document = append(document, member{"Parameters", parameters})
	}
	resources := make(object, 0, len(t.Resources))
	for _, r := range t.Resources {
		resource := object{{"Type", r.Type}}
		if r.Metadata != (Metadata{}) {
			resource = append(resource, member{"Metadata", r.Metadata})
		}
		if len(r.Properties) > 0 {
			resource = append(resource, member{"Properties", r.Properties})
		}
		resources = append(resources, member{r.ID, resource})
	}
	document = append(document, member{"Resources", resources})
	if len(t.Outputs) > 0 {
		outputs := make(object, 0, len(t.Outputs))
		for _, o := range t.Outputs {
			outputs = append(outputs, member{o.Name, object{{"Description", o.Description}, {"Value", o.Value}}})
		}
		document = append(document, member{"Outputs", outputs})
	}

	compact, err := encodeJSON(document)
	if err != nil {
		return "", fmt.Errorf("render the CloudFormation template: %w", err)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, compact, "", "  "); err != nil {
		return "", fmt.Errorf("render the CloudFormation template: %w", err)
	}
	return indented.String(), nil
}

type member struct {
	key   string
	value any
}

type object []member

func (o object) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			out.WriteByte(',')
		}
		key, err := encodeJSON(m.key)
		if err != nil {
			return nil, err
		}
		value, err := encodeJSON(m.value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m.key, err)
		}
		out.Write(key)
		out.WriteByte(':')
		out.Write(value)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

func encodeJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}
