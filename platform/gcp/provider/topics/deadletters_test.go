package topics_test

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

func (p published) deadLetter(payload string) string {
	p.t.Helper()
	failing := newFakeWorker(p.t, always(http.StatusInternalServerError, "no"))
	sent, err := p.deployment.Topics().Send(context.Background(), &topicv1.SendRequest{Topic: "orders", Payload: []byte(payload)})
	if err != nil {
		p.t.Fatal(err)
	}
	execution := sent.GetMessageId() + "-ship"
	pulled := p.pull("orders", "ship")
	if len(pulled) != 1 {
		p.t.Fatalf("the consumer's subscription holds %d messages, want 1", len(pulled))
	}
	deliveries := topics.Deliveries{Store: p.deployment.Store(), Topics: p.deployment.Declared, Worker: failing.url}
	for range 2 {
		req := httptestPush(pulled[0], "orders", "ship")
		deliveries.ServeHTTP(req.recorder, req.request)
	}
	run, err := p.deployment.Store().ReadRun(context.Background(), execution)
	if err != nil || run.Status != provider.RunFailed {
		p.t.Fatalf("the run after its last attempt = %+v, %v, want it failed", run, err)
	}
	return execution
}

func (p published) countDeadLetters() int64 {
	p.t.Helper()
	resp, err := p.deployment.Topics().CountDeadLetters(context.Background(), &topicv1.CountDeadLettersRequest{Topic: "orders", Consumer: "ship"})
	if err != nil {
		p.t.Fatalf("CountDeadLetters() = %v", err)
	}
	return resp.GetCount()
}

func TestLiveAConsumersFailedRunIsADeadLetterToListAndCount(t *testing.T) {
	p := newPublished(t)
	execution := p.deadLetter(exactJSON)

	resp, err := p.deployment.Topics().ListDeadLetters(context.Background(), &topicv1.ListDeadLettersRequest{Topic: "orders", Consumer: "ship"})
	if err != nil {
		t.Fatalf("ListDeadLetters() = %v", err)
	}
	if len(resp.GetDeadLetters()) != 1 {
		t.Fatalf("ListDeadLetters() = %d letters, want 1", len(resp.GetDeadLetters()))
	}
	letter := resp.GetDeadLetters()[0]
	if letter.GetExecution() != execution || string(letter.GetPayload()) != exactJSON || letter.GetAttempts() != 2 || letter.GetError() == "" || letter.GetFailedAt() == nil {
		t.Errorf("the dead letter is %+v, want %s with payload %s after 2 attempts, its error and failure time", letter, execution, exactJSON)
	}
	if letter.GetMessage().GetId() == "" || letter.GetMessage().GetPublishedAt() == nil {
		t.Errorf("the dead letter's message is %+v, want its id and publish time", letter.GetMessage())
	}
	if count := p.countDeadLetters(); count != 1 {
		t.Errorf("CountDeadLetters() = %d, want 1", count)
	}
	other, err := p.deployment.Topics().CountDeadLetters(context.Background(), &topicv1.CountDeadLettersRequest{Topic: "orders", Consumer: "digest"})
	if err != nil || other.GetCount() != 0 {
		t.Errorf("CountDeadLetters() of another consumer = %d, %v, want 0", other.GetCount(), err)
	}
}

func TestLiveARedrivenDeadLetterReachesItsConsumerAloneAndRunsAgain(t *testing.T) {
	p := newPublished(t)
	execution := p.deadLetter(exactJSON)
	p.pull("orders", "digest")

	resp, err := p.deployment.Topics().RedriveDeadLetters(context.Background(), &topicv1.RedriveDeadLettersRequest{Topic: "orders", Consumer: "ship"})
	if err != nil {
		t.Fatalf("RedriveDeadLetters() = %v", err)
	}
	if resp.GetRedriven() != 1 || p.countDeadLetters() != 0 {
		t.Errorf("RedriveDeadLetters() redrove %d and left %d, want 1 redriven and none left", resp.GetRedriven(), p.countDeadLetters())
	}
	run, err := p.deployment.Store().ReadRun(context.Background(), execution)
	if err != nil || run.Status != provider.RunQueued || run.Attempts != 0 || run.Error != "" {
		t.Errorf("the redriven run = %+v, %v, want it queued afresh", run, err)
	}
	pulled := p.pull("orders", "ship")
	if len(pulled) != 1 || pulled[0].payload() != exactJSON || pulled[0].Message.Attributes[topics.ConsumerAttribute] != "ship" {
		t.Fatalf("the consumer's subscription holds %d messages, want the redriven one naming ship", len(pulled))
	}

	worker := newFakeWorker(t, always(http.StatusOK, `{}`))
	deliveries := topics.Deliveries{Store: p.deployment.Store(), Topics: p.deployment.Declared, Worker: worker.url}
	req := httptestPush(pulled[0], "orders", "ship")
	deliveries.ServeHTTP(req.recorder, req.request)
	if run, err := p.deployment.Store().ReadRun(context.Background(), execution); err != nil || run.Status != provider.RunCompleted || run.Attempts != 1 {
		t.Errorf("the redriven run after delivery = %+v, %v, want it completed in one attempt", run, err)
	}

	if _, err := p.deployment.Topics().RedriveDeadLetters(context.Background(), &topicv1.RedriveDeadLettersRequest{Topic: "orders", Consumer: "nobody"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("RedriveDeadLetters() of no consumer = %v, want %s", err, connect.CodeNotFound)
	}
}

func TestLiveAPurgedDeadLetterIsGone(t *testing.T) {
	p := newPublished(t)
	kept := p.deadLetter(`{"n":1}`)
	purged := p.deadLetter(`{"n":2}`)

	resp, err := p.deployment.Topics().PurgeDeadLetters(context.Background(), &topicv1.PurgeDeadLettersRequest{Topic: "orders", Consumer: "ship", Executions: []string{purged}})
	if err != nil {
		t.Fatalf("PurgeDeadLetters() = %v", err)
	}
	if resp.GetPurged() != 1 {
		t.Errorf("PurgeDeadLetters() purged %d, want 1", resp.GetPurged())
	}
	listed, err := p.deployment.Topics().ListDeadLetters(context.Background(), &topicv1.ListDeadLettersRequest{Topic: "orders", Consumer: "ship"})
	if err != nil || len(listed.GetDeadLetters()) != 1 || listed.GetDeadLetters()[0].GetExecution() != kept {
		t.Errorf("ListDeadLetters() after the purge = %v, %v, want only %s", listed.GetDeadLetters(), err, kept)
	}

	all, err := p.deployment.Topics().PurgeDeadLetters(context.Background(), &topicv1.PurgeDeadLettersRequest{Topic: "orders", Consumer: "ship"})
	if err != nil || all.GetPurged() != 1 || p.countDeadLetters() != 0 {
		t.Errorf("PurgeDeadLetters() of every letter purged %d with %v, want the 1 left and none after", all.GetPurged(), err)
	}
}
