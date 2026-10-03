package proxy

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	PublishTimeout = 10 * time.Second
	maxAnswerBytes = 64 << 10
)

type Refusal struct {
	Transport string
	Status    int
	Answer    []byte
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("%s refused it with status %d: %s", r.Transport, r.Status, r.Answer)
}

func Send(client *http.Client, post *http.Request, transport string) ([]byte, error) {
	res, err := client.Do(post)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(res.Body, maxAnswerBytes))
	if res.StatusCode/100 != 2 {
		return nil, &Refusal{Transport: transport, Status: res.StatusCode, Answer: answer}
	}
	return answer, nil
}
