package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	runAppSuffix  = ".run.app"
	lookupTimeout = 10 * time.Second
)

var runAppHost = regexp.MustCompile(`^(?:([a-z0-9-]+)---)?([a-z0-9-]+)-[0-9]+\.([a-z0-9-]+)\.run\.app$`)

func parseRunAppHost(host string) (service, tag, region string, ok bool) {
	matched := runAppHost.FindStringSubmatch(host)
	if matched == nil {
		return "", "", "", false
	}
	return matched[2], matched[1], matched[3], true
}

type runService struct {
	URI             string `json:"uri"`
	TrafficStatuses []struct {
		Tag string `json:"tag"`
		URI string `json:"uri"`
	} `json:"trafficStatuses"`
}

func (d *dispatcher) resolveDeliveryURL(ctx context.Context, taskURL string) (string, error) {
	task, err := url.Parse(taskURL)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(task.Hostname(), runAppSuffix) {
		return taskURL, nil
	}
	d.lock.Lock()
	base, found := d.services[task.Host]
	d.lock.Unlock()
	if !found {
		if base, err = d.lookupService(ctx, task.Hostname()); err != nil {
			return "", err
		}
		d.lock.Lock()
		d.services[task.Host] = base
		d.lock.Unlock()
	}
	reached, err := url.Parse(d.reachable(base))
	if err != nil {
		return "", err
	}
	reached.Path, reached.RawQuery = task.Path, task.RawQuery
	return reached.String(), nil
}

func (d *dispatcher) forgetService(taskURL string) {
	task, err := url.Parse(taskURL)
	if err != nil {
		return
	}
	d.lock.Lock()
	delete(d.services, task.Host)
	d.lock.Unlock()
}

func (d *dispatcher) lookupService(ctx context.Context, host string) (string, error) {
	service, tag, region, ok := parseRunAppHost(host)
	if !ok {
		return "", fmt.Errorf("%s names no service this dispatcher can find", host)
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		d.endpoint+"/v2/projects/"+d.project+"/locations/"+region+"/services/"+service, nil)
	if err != nil {
		return "", err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("floci answered %d for service %s: %s", resp.StatusCode, service, body)
	}
	var found runService
	if err := json.Unmarshal(body, &found); err != nil {
		return "", err
	}
	if tag == "" {
		return found.URI, nil
	}
	for _, traffic := range found.TrafficStatuses {
		if traffic.Tag == tag && traffic.URI != "" {
			return traffic.URI, nil
		}
	}
	return "", fmt.Errorf("service %s: %w %q", service, errNoTagURL, tag)
}
