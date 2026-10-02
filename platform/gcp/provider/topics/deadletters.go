package topics

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"connectrpc.com/connect"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/timestamppb"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

func (t Topics) topicDeclaring(topicName, consumerName string) (*provider.TopicSpec, error) {
	topic, err := t.declared(topicName)
	if err != nil {
		return nil, err
	}
	for _, consumer := range topic.Consumers {
		if consumer.Name == consumerName {
			return topic, nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("topic %q has no consumer %q deployed", topicName, consumerName))
}

func (s Store) deadLetters(topic, consumer string) (firestore.Query, error) {
	collection, err := s.runCollection()
	if err != nil {
		return firestore.Query{}, err
	}
	return collection.Where("topic", "==", topic).Where("consumer", "==", consumer).Where("status", "==", string(provider.RunFailed)), nil
}

func (s Store) eachDeadLetter(ctx context.Context, topic, consumer string, executions []string, each func(storedRun) error) error {
	if len(executions) > 0 {
		return s.eachNamedDeadLetter(ctx, topic, consumer, executions, each)
	}
	query, err := s.deadLetters(topic, consumer)
	if err != nil {
		return err
	}
	documents := query.Documents(ctx)
	defer documents.Stop()
	for {
		snapshot, err := documents.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read the dead letters of %s/%s: %w", topic, consumer, err)
		}
		run, err := storedRunOf(snapshot)
		if err != nil {
			return err
		}
		if err := each(run); err != nil {
			return err
		}
	}
}

func (s Store) eachNamedDeadLetter(ctx context.Context, topic, consumer string, executions []string, each func(storedRun) error) error {
	collection, err := s.runCollection()
	if err != nil {
		return err
	}
	client, err := s.Clients.Firestore()
	if err != nil {
		return err
	}
	var docs []*firestore.DocumentRef
	for _, execution := range slices.Compact(slices.Sorted(slices.Values(executions))) {
		docs = append(docs, collection.Doc(execution))
	}
	snapshots, err := client.GetAll(ctx, docs)
	if err != nil {
		return fmt.Errorf("read the dead letters %v of %s/%s: %w", executions, topic, consumer, err)
	}
	for _, snapshot := range snapshots {
		if !snapshot.Exists() {
			continue
		}
		run, err := storedRunOf(snapshot)
		if err != nil {
			return err
		}
		if run.Topic != topic || run.Consumer != consumer || run.Status != provider.RunFailed {
			continue
		}
		if err := each(run); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) removeDeadLetter(ctx context.Context, execution string) (bool, error) {
	collection, err := s.runCollection()
	if err != nil {
		return false, err
	}
	client, err := s.Clients.Firestore()
	if err != nil {
		return false, err
	}
	doc := collection.Doc(execution)
	removed := false
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, found, err := readInTransaction(tx, doc)
		if err != nil || !found {
			return err
		}
		run, err := storedRunOf(snapshot)
		if err != nil || run.Status != provider.RunFailed {
			return err
		}
		removed = true
		return tx.Delete(doc)
	})
	if err != nil {
		return false, fmt.Errorf("purge dead letter %s: %w", execution, err)
	}
	return removed, nil
}

func (t Topics) ListDeadLetters(ctx context.Context, req *topicv1.ListDeadLettersRequest) (*topicv1.ListDeadLettersResponse, error) {
	query, err := t.deployment.Store().deadLetters(req.GetTopic(), req.GetConsumer())
	if err != nil {
		return nil, err
	}
	limit := runs.PageLimit(int(req.GetLimit()))
	query = query.OrderBy("finishedAt", firestore.Asc).OrderBy(firestore.DocumentID, firestore.Asc)
	if req.GetCursor() != "" {
		finished, execution, err := parseRunCursor(req.GetCursor())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		query = query.StartAfter(finished, execution)
	}
	documents := query.Limit(limit + 1).Documents(ctx)
	defer documents.Stop()
	resp := &topicv1.ListDeadLettersResponse{}
	var lastFinished time.Time
	for {
		snapshot, err := documents.Next()
		if errors.Is(err, iterator.Done) {
			return resp, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list dead letters: %w", err)
		}
		if len(resp.GetDeadLetters()) == limit {
			resp.NextCursor = runs.CursorOf(lastFinished, resp.GetDeadLetters()[limit-1].GetExecution())
			return resp, nil
		}
		run, err := storedRunOf(snapshot)
		if err != nil {
			return nil, err
		}
		lastFinished = run.FinishedAt
		resp.DeadLetters = append(resp.DeadLetters, deadLetterOf(run))
	}
}

func deadLetterOf(run storedRun) *topicv1.DeadLetter {
	message := &topicv1.Message{Id: run.delivery.MessageID}
	if run.delivery.PublishedAt != nil {
		message.PublishedAt = timestamppb.New(*run.delivery.PublishedAt)
	}
	return &topicv1.DeadLetter{
		Execution: run.Execution,
		Message:   message,
		Payload:   run.Payload,
		Attempts:  int32(run.Attempts),
		Error:     run.Error,
		FailedAt:  runs.TimestampOf(run.FinishedAt),
	}
}

func (t Topics) RedriveDeadLetters(ctx context.Context, req *topicv1.RedriveDeadLettersRequest) (*topicv1.RedriveDeadLettersResponse, error) {
	topic, err := t.topicDeclaring(req.GetTopic(), req.GetConsumer())
	if err != nil {
		return nil, err
	}
	store := t.deployment.Store()
	var redriven int64
	err = store.eachDeadLetter(ctx, req.GetTopic(), req.GetConsumer(), req.GetExecutions(), func(letter storedRun) error {
		now := time.Now()
		_, err := store.changeRun(ctx, letter.Execution, func(run *storedRun, found bool) error {
			if !found || run.Status != provider.RunFailed {
				return errRunUnchanged
			}
			run.Status, run.Attempts, run.Error, run.DueAt = provider.RunQueued, 0, "", now
			run.StartedAt, run.FinishedAt = time.Time{}, time.Time{}
			return nil
		})
		if errors.Is(err, errRunUnchanged) {
			return nil
		}
		if err != nil {
			return err
		}
		again := publicationOf(letter, topic)
		again.dueAt, again.consumer = now, req.GetConsumer()
		if err := t.deployment.publishNow(ctx, again); err != nil {
			return errors.Join(err, store.restoreDeadLetter(ctx, letter))
		}
		redriven++
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &topicv1.RedriveDeadLettersResponse{Redriven: redriven}, nil
}

func (s Store) restoreDeadLetter(ctx context.Context, letter storedRun) error {
	_, err := s.changeRun(ctx, letter.Execution, func(run *storedRun, found bool) error {
		if !found || run.Status != provider.RunQueued || run.Attempts != 0 {
			return errRunUnchanged
		}
		run.Status, run.Attempts, run.Error, run.DueAt = provider.RunFailed, letter.Attempts, letter.Error, letter.DueAt
		run.StartedAt, run.FinishedAt = letter.StartedAt, letter.FinishedAt
		return nil
	})
	if err != nil && !errors.Is(err, errRunUnchanged) {
		return fmt.Errorf("restore dead letter %s: %w", letter.Execution, err)
	}
	return nil
}

func (t Topics) PurgeDeadLetters(ctx context.Context, req *topicv1.PurgeDeadLettersRequest) (*topicv1.PurgeDeadLettersResponse, error) {
	store := t.deployment.Store()
	var purged int64
	err := store.eachDeadLetter(ctx, req.GetTopic(), req.GetConsumer(), req.GetExecutions(), func(letter storedRun) error {
		removed, err := store.removeDeadLetter(ctx, letter.Execution)
		if removed {
			purged++
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return &topicv1.PurgeDeadLettersResponse{Purged: purged}, nil
}

func (t Topics) CountDeadLetters(ctx context.Context, req *topicv1.CountDeadLettersRequest) (*topicv1.CountDeadLettersResponse, error) {
	query, err := t.deployment.Store().deadLetters(req.GetTopic(), req.GetConsumer())
	if err != nil {
		return nil, err
	}
	counted, err := query.NewAggregationQuery().WithCount("count").Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("count the dead letters of %s/%s: %w", req.GetTopic(), req.GetConsumer(), err)
	}
	count, _ := counted["count"].(*firestorepb.Value)
	return &topicv1.CountDeadLettersResponse{Count: count.GetIntegerValue()}, nil
}
