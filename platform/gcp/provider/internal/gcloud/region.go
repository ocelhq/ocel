package gcloud

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"

	runv1 "google.golang.org/api/run/v1"

	"github.com/ocelhq/ocel/pkg/provider/spelling"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const (
	runLocationsDoc        = "https://docs.cloud.google.com/run/docs/locations"
	RunLocationsPermission = "run.locations.list"
	runLocationsRole       = "roles/run.admin"
)

func (c *Clients) RefuseRegionNotServed(ctx context.Context) error {
	served, err := c.listRunRegions(ctx)
	if err != nil {
		return err
	}
	if slices.Contains(served, c.Region) {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid, "option %q names %q, which is not a region Cloud Run serves.%s Regions: %s. See %s",
		"region", c.Region, spelling.SuggestClosest(c.Region, served), strings.Join(served, ", "), runLocationsDoc)
}

func (c *Clients) listRunRegions(ctx context.Context) ([]string, error) {
	service, err := c.RunLocations()
	if err != nil {
		return nil, err
	}
	var served []string
	token := ""
	for {
		call := service.Projects.Locations.List("projects/" + c.Project()).Context(ctx)
		if token != "" {
			call = call.PageToken(token)
		}
		page, err := googleretry.Attempted(ctx, call.Do)
		if googleretry.AnsweredCode(err) == http.StatusForbidden {
			return nil, refusal.Refuse(refusal.CodeDenied,
				"this credential may not list the regions Cloud Run serves project %s from, which every run checks its region against: it lacks %s.\n"+
					"Granting %s covers it, but the permission is what is checked",
				c.Project(), RunLocationsPermission, runLocationsRole)
		}
		if err != nil {
			return nil, fmt.Errorf("list the regions Cloud Run serves project %s from, to check the %q it was given: %w", c.Project(), c.Region, err)
		}
		for _, location := range page.Locations {
			served = append(served, runRegion(location))
		}
		if token = page.NextPageToken; token == "" {
			slices.Sort(served)
			return served, nil
		}
	}
}

func runRegion(location *runv1.Location) string {
	if location.LocationId != "" {
		return location.LocationId
	}
	return path.Base(location.Name)
}
