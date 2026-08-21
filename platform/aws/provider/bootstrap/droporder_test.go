package bootstrap

import (
	"context"
	"testing"
)

func TestAFeatureIsDroppedBeforeWhatRendersAgainstIt(t *testing.T) {
	cfn, apis := standingBootstrap(t)
	edge, dropped := edgeStack(ClassProduction), optStack(ClassProduction)

	if err := Run(context.Background(), apis, Request{Features: []string{FeatureISR, FeatureCloudflareEdge}}, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	removal, write := cfn.lastEvent("removed "+dropped), cfn.lastEvent("wrote "+edge)
	if removal < 0 {
		t.Fatalf("%s was left standing though the set no longer names it", dropped)
	}
	if write < 0 {
		t.Fatalf("%s was never rewritten for the set it now stands alongside", edge)
	}
	if removal > write {
		t.Errorf("%s was rewritten before %s went, so an interrupted run leaves it reading against a set that no longer stands", edge, dropped)
	}

	deployed, err := CheckDeployed(context.Background(), cfn)
	if err != nil {
		t.Fatalf("CheckDeployed: %v", err)
	}
	if stale := deployed.Stale([]string{FeatureISR, FeatureCloudflareEdge}); len(stale) != 0 {
		t.Errorf("a bootstrap this run just wrote reads as stale: %+v", stale)
	}
}
