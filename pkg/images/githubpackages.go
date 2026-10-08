package images

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
)

const (
	gitHubContainerRegistry = "ghcr.io"
	gitHubAPI               = "https://api.github.com"

	packageVersionsPerPage = 100
	packageVersionPages    = 100
)

type packageVersion struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Metadata struct {
		Container struct {
			Tags []string `json:"tags"`
		} `json:"container"`
	} `json:"metadata"`
}

type gitHubPackages struct {
	client *http.Client
	token  string
}

type gitHubAnswer struct {
	status  int
	body    []byte
	message string
}

func (r registryStore) removePackageVersion(ctx context.Context, tag name.Tag) error {
	owner, packageName, nested := strings.Cut(tag.RepositoryStr(), "/")
	if !nested {
		return fmt.Errorf("%s names no package under a GitHub owner", tag)
	}
	api := gitHubPackages{client: &http.Client{Timeout: registryTimeout}, token: r.target.Password}
	for _, scope := range []string{"orgs/", "users/"} {
		packagePath := strings.TrimSuffix(r.packagesAPI, "/") + "/" + scope + url.PathEscape(owner) + "/packages/container/" + url.PathEscape(packageName)
		found, err := api.findVersion(ctx, packagePath, tag.TagStr())
		if err != nil {
			return fmt.Errorf("look for %s among the versions of GitHub package %s: %w", tag, packageName, err)
		}
		if !found.listed {
			continue
		}
		if found.version == nil || slices.ContainsFunc(found.version.Metadata.Container.Tags, func(other string) bool { return other != tag.TagStr() }) {
			return nil
		}
		return api.deleteVersion(ctx, packagePath, *found.version, found.sole, tag)
	}
	return nil
}

type foundVersion struct {
	listed  bool
	version *packageVersion
	sole    bool
}

func (g gitHubPackages) findVersion(ctx context.Context, packagePath, tag string) (foundVersion, error) {
	listed := 0
	var found *packageVersion
	for page := 1; page <= packageVersionPages; page++ {
		versions, present, err := g.listVersions(ctx, packagePath, packageVersionsPerPage, page)
		if err != nil || !present {
			return foundVersion{}, err
		}
		listed += len(versions)
		for _, version := range versions {
			if found == nil && slices.Contains(version.Metadata.Container.Tags, tag) {
				found = &version
			}
		}
		if found != nil && listed > 1 || len(versions) < packageVersionsPerPage {
			break
		}
	}
	return foundVersion{listed: true, version: found, sole: found != nil && listed == 1}, nil
}

func (g gitHubPackages) listVersions(ctx context.Context, packagePath string, perPage, page int) ([]packageVersion, bool, error) {
	answer, err := g.call(ctx, http.MethodGet, packagePath+"/versions?per_page="+strconv.Itoa(perPage)+"&page="+strconv.Itoa(page))
	if err != nil {
		return nil, false, err
	}
	switch answer.status {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, false, nil
	default:
		return nil, false, answer.refusal("listing package versions")
	}
	var versions []packageVersion
	if err := json.Unmarshal(answer.body, &versions); err != nil {
		return nil, false, fmt.Errorf("read the package versions GitHub listed: %w", err)
	}
	return versions, true, nil
}

func (g gitHubPackages) deleteVersion(ctx context.Context, packagePath string, version packageVersion, sole bool, tag name.Tag) error {
	if sole {
		stillSole, err := g.isSoleVersion(ctx, packagePath, version.ID)
		if err != nil {
			return fmt.Errorf("look for other versions of the package %s before deleting it: %w", tag.Repository, err)
		}
		if stillSole {
			return g.deletePackage(ctx, packagePath, tag)
		}
	}
	answer, err := g.call(ctx, http.MethodDelete, packagePath+"/versions/"+strconv.FormatInt(version.ID, 10))
	if err != nil {
		return fmt.Errorf("remove %s from GitHub Packages: %w", tag, err)
	}
	switch answer.status {
	case http.StatusNoContent, http.StatusNotFound:
		return nil
	case http.StatusBadRequest:
		stillSole, err := g.isSoleVersion(ctx, packagePath, version.ID)
		if err != nil {
			return fmt.Errorf("look for other versions of the package %s after GitHub refused to delete %s: %w", tag.Repository, tag, err)
		}
		if stillSole {
			return g.deletePackage(ctx, packagePath, tag)
		}
	}
	return fmt.Errorf("remove %s from GitHub Packages: %w", tag, answer.refusal("deleting a package version"))
}

func (g gitHubPackages) isSoleVersion(ctx context.Context, packagePath string, id int64) (bool, error) {
	versions, present, err := g.listVersions(ctx, packagePath, 2, 1)
	if err != nil || !present {
		return false, err
	}
	return len(versions) == 1 && versions[0].ID == id, nil
}

func (g gitHubPackages) deletePackage(ctx context.Context, packagePath string, tag name.Tag) error {
	answer, err := g.call(ctx, http.MethodDelete, packagePath)
	if err != nil {
		return fmt.Errorf("remove the package %s's last version, %s, lives in: %w", tag.Repository, tag, err)
	}
	if answer.status == http.StatusNoContent || answer.status == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("remove the package %s, whose last version is %s: %w", tag.Repository, tag, answer.refusal("deleting a package"))
}

func (a gitHubAnswer) refusal(doing string) error {
	if a.status == http.StatusForbidden || a.status == http.StatusUnauthorized {
		return fmt.Errorf("GitHub answered %d %s: the registry password is a token that needs the read:packages and delete:packages scopes, and admin on the package: %s",
			a.status, doing, a.message)
	}
	return fmt.Errorf("GitHub answered %d %s: %s", a.status, doing, a.message)
}

func (g gitHubPackages) call(ctx context.Context, method, endpoint string) (gitHubAnswer, error) {
	var (
		wait      time.Duration
		answer    gitHubAnswer
		throttled bool
		err       error
	)
	for attempt := range registryAttempts {
		if attempt > 0 {
			if err := pause(ctx, backoff(attempt, wait)); err != nil {
				return gitHubAnswer{}, err
			}
		}
		answer, throttled, wait, err = g.attempt(ctx, method, endpoint)
		if err == nil && !throttled {
			return answer, nil
		}
		if err != nil && !resolvable(err) {
			return gitHubAnswer{}, err
		}
	}
	return answer, err
}

func (g gitHubPackages) attempt(ctx context.Context, method, endpoint string) (gitHubAnswer, bool, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return gitHubAnswer{}, false, 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return gitHubAnswer{}, false, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return gitHubAnswer{}, false, 0, err
	}
	answer := gitHubAnswer{status: resp.StatusCode, body: body, message: strings.TrimSpace(string(body))}
	var said struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &said) == nil && said.Message != "" {
		answer.message = said.Message
	}
	return answer, isThrottled(resp), retryAfter(resp), nil
}

func isThrottled(resp *http.Response) bool {
	switch {
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= http.StatusInternalServerError:
		return true
	case resp.StatusCode == http.StatusForbidden:
		return resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0"
	}
	return false
}
