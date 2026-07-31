package retrieval_test

import (
	"context"
	"testing"

	"lore/internal/retrievaleval"
)

func TestCheckedInRetrievalBaseline(t *testing.T) {
	report, err := retrievaleval.Run(context.Background(), retrievaleval.RunOptions{
		SuitePath:   "suite.yaml",
		LoreVersion: "retrieval-eval-test",
	})
	if err != nil {
		t.Fatalf("run retrieval evaluation: %v", err)
	}
	if retrievaleval.HasAuthorizationFailure(report) {
		t.Fatal("retrieval evaluation returned a forbidden document")
	}
	baseline, err := retrievaleval.LoadReport("baseline.json")
	if err != nil {
		t.Fatalf("load retrieval baseline: %v", err)
	}
	if differences := retrievaleval.DiffReports(baseline, report); len(differences) != 0 {
		for _, difference := range differences {
			t.Log(difference)
		}
		t.Fatal("retrieval behavior differs from baseline; review it with go run ./cmd/lore-retrieval-eval")
	}
}
