package ocel

import (
	"fmt"
	"net/http"
)

type answer struct {
	status      int
	contentType string
	body        []byte
}

func (a answer) write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", a.contentType)
	w.WriteHeader(a.status)
	_, _ = w.Write(a.body)
}

func newRefusalAnswer(status int, format string, args ...any) answer {
	return answer{status: status, contentType: "text/plain; charset=utf-8", body: []byte(fmt.Sprintf(format, args...))}
}

func newRetryAnswer(err error) answer {
	return newRefusalAnswer(http.StatusInternalServerError, "%s", err.Error())
}

func newAbortAnswer(reason string) answer {
	body, _ := encodeJSON(map[string]map[string]string{"abort": {"reason": reason}})
	return answer{status: http.StatusUnprocessableEntity, contentType: "application/json", body: body}
}

func newSuccessAnswer(output []byte) answer {
	return answer{status: http.StatusOK, contentType: "application/json", body: output}
}
