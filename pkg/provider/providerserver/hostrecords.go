package providerserver

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/seal"
)

const (
	RootSessions  records.Root = "providersessions"
	RootQuestions records.Root = "providerquestions"

	sessionPurpose  = "provider-session"
	questionPurpose = "provider-question"
)

func SessionsPartition() records.Partition {
	return records.Partition{Tier: environment.TierProduction, Root: RootSessions}
}

func SessionKey(tenant, id string) records.Key { return SessionsPartition().Key(tenant, id) }

func SessionAssociatedData(tenant, id string) seal.AssociatedData {
	return seal.Bind(sessionPurpose, tenant, id)
}

func QuestionsPartition(tier environment.Tier) records.Partition {
	return records.Partition{Tier: tier, Root: RootQuestions}
}

func QuestionKey(tier environment.Tier, tenant, session, question string) records.Key {
	return QuestionsPartition(tier).Key(tenant, session, question)
}

func QuestionAssociatedData(tenant, session, question string) seal.AssociatedData {
	return seal.Bind(questionPurpose, tenant, session, question)
}

func ownerOfHostRecord(key records.Key) (records.Owner, bool) {
	switch {
	case key.Partition.Root == RootSessions && len(key.Path) == 2:
		return records.Owner{Shared: true}, true
	case key.Partition.Root == RootQuestions && len(key.Path) == 3:
		return records.Owner{Shared: true}, true
	}
	return records.Owner{}, false
}

func ownerOfHostAssociatedData(bound seal.AssociatedData) (records.Owner, bool) {
	switch bound.Purpose() {
	case sessionPurpose, questionPurpose:
		return records.Owner{Shared: true}, true
	}
	return records.Owner{}, false
}
