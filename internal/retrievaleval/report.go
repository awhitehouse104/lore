package retrievaleval

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

func LoadReport(path string) (Report, error) {
	file, err := os.Open(path)
	if err != nil {
		return Report{}, fmt.Errorf("open retrieval baseline: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var report Report
	if err := decoder.Decode(&report); err != nil {
		return Report{}, fmt.Errorf("decode retrieval baseline: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Report{}, fmt.Errorf("decode retrieval baseline: multiple JSON values are not allowed")
		}
		return Report{}, fmt.Errorf("decode retrieval baseline: %w", err)
	}
	if report.SchemaVersion != ReportSchemaVersion {
		return Report{}, fmt.Errorf("retrieval baseline schema_version must equal %d", ReportSchemaVersion)
	}
	return report, nil
}

func WriteReport(path string, report Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode retrieval baseline: %w", err)
	}
	data = append(data, '\n')
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".retrieval-baseline-*")
	if err != nil {
		return fmt.Errorf("create retrieval baseline temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o644); err != nil {
		cleanup()
		return fmt.Errorf("set retrieval baseline permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write retrieval baseline: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("flush retrieval baseline: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("close retrieval baseline: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("replace retrieval baseline: %w", err)
	}
	return nil
}

func WriteJSON(writer io.Writer, report Report) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func RenderText(writer io.Writer, report Report) {
	fmt.Fprintf(writer, "Retrieval evaluation: %s\n", report.Suite)
	fmt.Fprintf(writer, "Corpus: %d documents; cases: %d\n\n", report.CorpusDocuments, report.CaseCount)
	for _, backend := range report.Backends {
		fmt.Fprintf(writer, "%s backend (effective: %d filesystem, %d index)\n",
			backend.RequestedBackend, backend.FilesystemCases, backend.IndexedCases)
		renderMetrics(writer, "overall", backend.Overall)
		for _, category := range backend.Categories {
			renderMetrics(writer, category.Category, category.Metrics)
		}
		fmt.Fprintln(writer)
	}
	fmt.Fprintf(writer, "Parity: %d/%d cases equal", report.Parity.EqualCases, report.Parity.ComparedCases)
	if len(report.Parity.Mismatches) == 0 {
		fmt.Fprintln(writer)
	} else {
		fmt.Fprintf(writer, " (%d mismatches)\n", len(report.Parity.Mismatches))
		for _, mismatch := range report.Parity.Mismatches {
			fmt.Fprintf(writer, "  %s: filesystem=%s auto=%s\n", mismatch.CaseID, resultIDs(mismatch.Filesystem, 5), resultIDs(mismatch.Auto, 5))
		}
	}

	production := backendByName(report, string(searchBackendAuto))
	if production == nil {
		return
	}
	fmt.Fprintln(writer, "\nWeak or missed production-auto cases:")
	weak := 0
	for _, testCase := range production.Cases {
		if testCase.FirstRelevantRank > 0 && testCase.FirstRelevantRank <= 5 {
			continue
		}
		weak++
		fmt.Fprintf(writer, "  %s/%s rank=%d query=%q expected=%s top=%s\n",
			testCase.Category, testCase.ID, testCase.FirstRelevantRank, testCase.Query.Text,
			relevantIDs(testCase.Relevant), resultIDs(testCase.Results, 3))
	}
	if weak == 0 {
		fmt.Fprintln(writer, "  none")
	}
}

// Avoid importing the search package solely for one report lookup constant.
const searchBackendAuto = "auto"

func renderMetrics(writer io.Writer, label string, metrics Metrics) {
	fmt.Fprintf(writer,
		"  %-22s n=%2d hit@1=%5.1f%% hit@3=%5.1f%% hit@5=%5.1f%% hit@10=%5.1f%% MRR=%.3f recall@5=%.3f nDCG@10=%.3f zero=%d forbidden=%d\n",
		label, metrics.Cases, percentage(metrics.HitAt1, metrics.Cases), percentage(metrics.HitAt3, metrics.Cases),
		percentage(metrics.HitAt5, metrics.Cases), percentage(metrics.HitAt10, metrics.Cases),
		metrics.MeanReciprocalRank, metrics.MeanRecallAt5, metrics.MeanNDCGAt10,
		metrics.ZeroResults, metrics.ForbiddenResults,
	)
}

func percentage(value, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(value) * 100 / float64(total)
}

func DiffReports(want, got Report) []string {
	if reflect.DeepEqual(want, got) {
		return nil
	}
	differences := make([]string, 0)
	if want.SchemaVersion != got.SchemaVersion || want.SuiteVersion != got.SuiteVersion || want.Suite != got.Suite ||
		want.CorpusDocuments != got.CorpusDocuments || want.CaseCount != got.CaseCount {
		differences = append(differences, fmt.Sprintf(
			"suite metadata changed: baseline=%s/v%d/%d-docs/%d-cases current=%s/v%d/%d-docs/%d-cases",
			want.Suite, want.SuiteVersion, want.CorpusDocuments, want.CaseCount,
			got.Suite, got.SuiteVersion, got.CorpusDocuments, got.CaseCount,
		))
	}
	for _, currentBackend := range got.Backends {
		baselineBackend := backendByName(want, currentBackend.RequestedBackend)
		if baselineBackend == nil {
			differences = append(differences, "backend added: "+currentBackend.RequestedBackend)
			continue
		}
		if !reflect.DeepEqual(baselineBackend.Overall, currentBackend.Overall) {
			differences = append(differences, fmt.Sprintf("%s overall metrics changed: baseline=%s current=%s",
				currentBackend.RequestedBackend, compactMetrics(baselineBackend.Overall), compactMetrics(currentBackend.Overall)))
		}
		baselineCases := make(map[string]CaseReport, len(baselineBackend.Cases))
		for _, testCase := range baselineBackend.Cases {
			baselineCases[testCase.ID] = testCase
		}
		for _, currentCase := range currentBackend.Cases {
			baselineCase, exists := baselineCases[currentCase.ID]
			if !exists {
				differences = append(differences, fmt.Sprintf("%s case added: %s", currentBackend.RequestedBackend, currentCase.ID))
				continue
			}
			if !reflect.DeepEqual(baselineCase, currentCase) {
				differences = append(differences, fmt.Sprintf(
					"%s case %s changed: rank %d -> %d; results %s -> %s",
					currentBackend.RequestedBackend, currentCase.ID,
					baselineCase.FirstRelevantRank, currentCase.FirstRelevantRank,
					resultIDs(baselineCase.Results, 5), resultIDs(currentCase.Results, 5),
				))
			}
			if len(differences) >= 25 {
				return append(differences, "additional differences omitted")
			}
		}
	}
	if !reflect.DeepEqual(want.Parity, got.Parity) {
		differences = append(differences, fmt.Sprintf("backend parity changed: %d/%d -> %d/%d",
			want.Parity.EqualCases, want.Parity.ComparedCases, got.Parity.EqualCases, got.Parity.ComparedCases))
	}
	if len(differences) == 0 {
		differences = append(differences, "report changed outside summarized fields")
	}
	return differences
}

func HasAuthorizationFailure(report Report) bool {
	for _, backend := range report.Backends {
		if backend.Overall.ForbiddenResults != 0 {
			return true
		}
	}
	return false
}

func backendByName(report Report, name string) *BackendReport {
	for index := range report.Backends {
		if report.Backends[index].RequestedBackend == name {
			return &report.Backends[index]
		}
	}
	return nil
}

func compactMetrics(metrics Metrics) string {
	return fmt.Sprintf("hit@1=%d/%d hit@5=%d/%d MRR=%.3f nDCG@10=%.3f zero=%d forbidden=%d",
		metrics.HitAt1, metrics.Cases, metrics.HitAt5, metrics.Cases,
		metrics.MeanReciprocalRank, metrics.MeanNDCGAt10, metrics.ZeroResults, metrics.ForbiddenResults)
}

func resultIDs(results []RankedResult, limit int) string {
	if len(results) == 0 {
		return "[]"
	}
	if len(results) < limit {
		limit = len(results)
	}
	ids := make([]string, 0, limit)
	for _, result := range results[:limit] {
		ids = append(ids, result.ID)
	}
	return "[" + strings.Join(ids, ",") + "]"
}

func relevantIDs(results []RelevantResult) string {
	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.ID)
	}
	return "[" + strings.Join(ids, ",") + "]"
}
