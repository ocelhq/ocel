package gcloud

import (
	"errors"
	"net/http"

	"github.com/googleapis/gax-go/v2/apierror"
	"google.golang.org/api/googleapi"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const serviceDisabled = "SERVICE_DISABLED"

func Absent(err error) bool {
	var answered *googleapi.Error
	return errors.As(err, &answered) && answered.Code == http.StatusNotFound
}

func Taken(err error) bool { return googleretry.AnsweredCode(err) == http.StatusConflict }

func Forbidden(err error) bool { return googleretry.AnsweredCode(err) == http.StatusForbidden }

func AnsweredReason(err error) string {
	var parsed *apierror.APIError
	if !errors.As(err, &parsed) {
		parsed, _ = apierror.ParseError(err, false)
	}
	if parsed == nil {
		return ""
	}
	return parsed.Reason()
}

func ServiceDisabled(err error) bool { return AnsweredReason(err) == serviceDisabled }
