package variables

import (
	"fmt"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func RefusePublishedClass(definitions []*resourcesv1.VariableDefinition, row ValueMetadata) error {
	key := row.Cell.Key
	for _, definition := range definitions {
		if definition.GetKey() != key {
			continue
		}
		declaredSecret := definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET
		switch {
		case row.PublishedSecret && !declaredSecret:
			return fmt.Errorf("%s is published as a secret by %s, and %s declares it %s, so a deploy would write it into the release: declare it as secret, or publish it as a plain value",
				describePublished(row.Coordinate), row.Publisher, source(definition), ClassName(definition.GetClass()))
		case !row.PublishedSecret && declaredSecret:
			return fmt.Errorf("%s is declared secret in %s, and %s publishes it as a plain value, unprotected in the program's own state: publish it as a secret, or declare it as plain or sensitive",
				describePublished(row.Coordinate), source(definition), row.Publisher)
		}
		return nil
	}
	return nil
}

func describePublished(at Coordinate) string {
	if at.Environment == "" {
		return at.Cell.Key
	}
	return at.Cell.Key + " in " + at.Environment
}

func (d *Declarations) refusePublishedClasses(apps []App, definitions []*resourcesv1.VariableDefinition) error {
	read := map[Coordinate]bool{}
	for _, app := range apps {
		cells, _, err := d.cellsOf(app.Name)
		if err != nil {
			return err
		}
		d.mu.Lock()
		for _, c := range cells {
			read[Coordinate{Cell: c.cell, Environment: d.resolvedEnvironment(c.cell)}] = true
		}
		d.mu.Unlock()
	}
	d.mu.Lock()
	published := d.published
	d.mu.Unlock()
	for _, row := range published {
		if !read[row.Coordinate] {
			continue
		}
		if err := RefusePublishedClass(definitions, row); err != nil {
			return err
		}
	}
	return nil
}
