package gcloud

import (
	"errors"
	"net/http"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"google.golang.org/api/googleapi"
)

func Absent(err error) bool {
	var answered *googleapi.Error
	return errors.As(err, &answered) && answered.Code == http.StatusNotFound
}

func Taken(err error) bool { return googleretry.AnsweredCode(err) == http.StatusConflict }
