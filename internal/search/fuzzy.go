package search

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"lore/internal/catalog"
)

type MatchingMode string

const (
	MatchingAuto    MatchingMode = "auto"
	MatchingLexical MatchingMode = "lexical"
	MatchingFuzzy   MatchingMode = "fuzzy"

	MinimumFuzzyTokenRunes      = 4
	MinimumAutoFuzzyTokenRunes  = 6
	MaximumFuzzyTokenRunes      = 24
	MaximumFuzzyQueryTokens     = 8
	MaximumFuzzyExpansions      = 4
	MaximumFuzzyVocabularyTerms = 100_000
)

type FuzzyMatch struct {
	QueryTerm    string `json:"query_term"`
	DocumentTerm string `json:"document_term"`
	Distance     int    `json:"distance"`
}

type TermExpansion struct {
	QueryTerm         string
	DocumentTerm      string
	Distance          int
	DocumentFrequency int
}

type VocabularyTerm struct {
	Term              string
	DocumentFrequency int
}

type FuzzyTarget struct {
	Term        string
	Position    int
	RuneLength  int
	MaxDistance int
}

type FuzzyPlan struct {
	Targets   []FuzzyTarget
	Truncated bool
}

type MatchingError struct {
	Code    string
	Message string
}

func (e *MatchingError) Error() string {
	return e.Message
}

func NormalizeMatching(mode MatchingMode) MatchingMode {
	if mode == "" {
		return MatchingAuto
	}
	return mode
}

func PlanFuzzyExpansion(tokens []string, mode MatchingMode, documentFrequency map[string]int) (FuzzyPlan, error) {
	mode = NormalizeMatching(mode)
	switch mode {
	case MatchingAuto, MatchingLexical, MatchingFuzzy:
	default:
		return FuzzyPlan{}, &MatchingError{Code: "invalid_matching", Message: "search matching must be auto, lexical, or fuzzy"}
	}
	if mode == MatchingLexical {
		return FuzzyPlan{Targets: []FuzzyTarget{}}, nil
	}
	targets := make([]FuzzyTarget, 0, len(tokens))
	for position, token := range tokens {
		length := utf8.RuneCountInString(token)
		if length < MinimumFuzzyTokenRunes || length > MaximumFuzzyTokenRunes {
			continue
		}
		if mode == MatchingAuto && length < MinimumAutoFuzzyTokenRunes {
			continue
		}
		if mode == MatchingAuto && documentFrequency[token] != 0 {
			continue
		}
		distance := 1
		if length >= 8 {
			distance = 2
		}
		targets = append(targets, FuzzyTarget{
			Term: token, Position: position, RuneLength: length, MaxDistance: distance,
		})
	}
	if len(targets) <= MaximumFuzzyQueryTokens {
		return FuzzyPlan{Targets: targets}, nil
	}
	if mode == MatchingFuzzy {
		return FuzzyPlan{}, &MatchingError{
			Code:    "fuzzy_query_too_broad",
			Message: fmt.Sprintf("fuzzy matching supports at most %d eligible query terms", MaximumFuzzyQueryTokens),
		}
	}
	selected := append([]FuzzyTarget(nil), targets...)
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].RuneLength != selected[j].RuneLength {
			return selected[i].RuneLength > selected[j].RuneLength
		}
		return selected[i].Position < selected[j].Position
	})
	selected = selected[:MaximumFuzzyQueryTokens]
	sort.Slice(selected, func(i, j int) bool { return selected[i].Position < selected[j].Position })
	return FuzzyPlan{Targets: selected, Truncated: true}, nil
}

func FuzzyCandidateLengths(plan FuzzyPlan) []int {
	seen := map[int]struct{}{}
	for _, target := range plan.Targets {
		minimum := target.RuneLength - target.MaxDistance
		if minimum < MinimumFuzzyTokenRunes {
			minimum = MinimumFuzzyTokenRunes
		}
		for length := minimum; length <= target.RuneLength+target.MaxDistance; length++ {
			seen[length] = struct{}{}
		}
	}
	lengths := make([]int, 0, len(seen))
	for length := range seen {
		lengths = append(lengths, length)
	}
	sort.Ints(lengths)
	return lengths
}

func SelectFuzzyExpansions(plan FuzzyPlan, vocabulary []VocabularyTerm) []TermExpansion {
	expansions := make([]TermExpansion, 0, len(plan.Targets)*MaximumFuzzyExpansions)
	for _, target := range plan.Targets {
		matches := make([]TermExpansion, 0, MaximumFuzzyExpansions)
		for _, candidate := range vocabulary {
			candidateLength := utf8.RuneCountInString(candidate.Term)
			if absolute(candidateLength-target.RuneLength) > target.MaxDistance {
				continue
			}
			distance := boundedDamerauLevenshtein(target.Term, candidate.Term, target.MaxDistance)
			if distance <= 0 || distance > target.MaxDistance {
				continue
			}
			if !acceptableFuzzySimilarity(target.RuneLength, candidateLength, distance) {
				continue
			}
			match := TermExpansion{
				QueryTerm: target.Term, DocumentTerm: candidate.Term,
				Distance: distance, DocumentFrequency: candidate.DocumentFrequency,
			}
			matches = insertPreferredExpansion(matches, match, target.RuneLength)
		}
		expansions = append(expansions, matches...)
	}
	return expansions
}

func acceptableFuzzySimilarity(queryLength, candidateLength, distance int) bool {
	minimum := 800
	if queryLength < 8 {
		minimum = 750
	}
	return fuzzySimilarity(queryLength, candidateLength, distance) >= minimum
}

func insertPreferredExpansion(matches []TermExpansion, candidate TermExpansion, queryLength int) []TermExpansion {
	if len(matches) == MaximumFuzzyExpansions && !preferredExpansion(candidate, matches[len(matches)-1], queryLength) {
		return matches
	}
	if len(matches) < MaximumFuzzyExpansions {
		matches = append(matches, candidate)
	} else {
		matches[len(matches)-1] = candidate
	}
	for index := len(matches) - 1; index > 0; index-- {
		if !preferredExpansion(matches[index], matches[index-1], queryLength) {
			break
		}
		matches[index], matches[index-1] = matches[index-1], matches[index]
	}
	return matches
}

func preferredExpansion(left, right TermExpansion, queryLength int) bool {
	if left.Distance != right.Distance {
		return left.Distance < right.Distance
	}
	leftSimilarity := fuzzySimilarity(queryLength, utf8.RuneCountInString(left.DocumentTerm), left.Distance)
	rightSimilarity := fuzzySimilarity(queryLength, utf8.RuneCountInString(right.DocumentTerm), right.Distance)
	if leftSimilarity != rightSimilarity {
		return leftSimilarity > rightSimilarity
	}
	if left.DocumentFrequency != right.DocumentFrequency {
		return left.DocumentFrequency > right.DocumentFrequency
	}
	return left.DocumentTerm < right.DocumentTerm
}

func ExtendMatchExpression(base string, expansions []TermExpansion) string {
	terms := make([]string, 0, len(expansions))
	seen := map[string]struct{}{}
	for _, expansion := range expansions {
		if _, exists := seen[expansion.DocumentTerm]; exists {
			continue
		}
		seen[expansion.DocumentTerm] = struct{}{}
		terms = append(terms, expansion.DocumentTerm)
	}
	sort.Strings(terms)
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		parts = append(parts, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}
	if len(parts) == 0 {
		return base
	}
	return base + " OR " + strings.Join(parts, " OR ")
}

func prepareFuzzyExpansion(
	candidates []Candidate,
	queryTokens []string,
	mode MatchingMode,
) (map[string]int, []TermExpansion, []catalog.Warning, error) {
	return prepareFuzzyExpansionWithLimit(candidates, queryTokens, mode, MaximumFuzzyVocabularyTerms)
}

func prepareFuzzyExpansionWithLimit(
	candidates []Candidate,
	queryTokens []string,
	mode MatchingMode,
	maximumVocabularyTerms int,
) (map[string]int, []TermExpansion, []catalog.Warning, error) {
	documentFrequency := make(map[string]int, len(queryTokens))
	querySet := make(map[string]struct{}, len(queryTokens))
	for _, token := range queryTokens {
		querySet[token] = struct{}{}
	}
	for _, candidate := range candidates {
		terms := DocumentTokens(candidate.Title, candidate.Aliases, candidate.Tags, candidate.Kind, candidate.Body)
		for _, term := range terms {
			if _, exists := querySet[term]; exists {
				documentFrequency[term]++
			}
		}
	}
	plan, err := PlanFuzzyExpansion(queryTokens, mode, documentFrequency)
	if err != nil {
		return nil, nil, nil, err
	}
	warnings := make([]catalog.Warning, 0, 1)
	if plan.Truncated {
		warnings = append(warnings, catalog.Warning{
			Code:    "fuzzy_token_limit",
			Path:    "search",
			Message: fmt.Sprintf("automatic fuzzy matching considered the %d longest eligible query terms", MaximumFuzzyQueryTokens),
		})
	}
	if len(plan.Targets) == 0 {
		return documentFrequency, []TermExpansion{}, warnings, nil
	}
	lengthSet := map[int]struct{}{}
	for _, length := range FuzzyCandidateLengths(plan) {
		lengthSet[length] = struct{}{}
	}
	vocabularyFrequency := map[string]int{}
	limitReached := false
	for _, candidate := range candidates {
		terms := DocumentTokens(candidate.Title, candidate.Aliases, candidate.Tags, candidate.Kind, candidate.Body)
		for _, term := range terms {
			if _, eligible := lengthSet[utf8.RuneCountInString(term)]; !eligible {
				continue
			}
			if _, exists := vocabularyFrequency[term]; !exists && len(vocabularyFrequency) >= maximumVocabularyTerms {
				limitReached = true
				break
			}
			vocabularyFrequency[term]++
		}
		if limitReached {
			break
		}
	}
	if limitReached {
		if NormalizeMatching(mode) == MatchingFuzzy {
			return nil, nil, nil, &MatchingError{
				Code:    "fuzzy_vocabulary_limit",
				Message: fmt.Sprintf("filtered fuzzy vocabulary exceeds the supported bound of %d terms", maximumVocabularyTerms),
			}
		}
		warnings = append(warnings, catalog.Warning{
			Code:    "fuzzy_vocabulary_limit",
			Path:    "search",
			Message: "automatic fuzzy expansion was skipped because the filtered vocabulary exceeded its work bound",
		})
		return documentFrequency, []TermExpansion{}, warnings, nil
	}
	vocabulary := make([]VocabularyTerm, 0, len(vocabularyFrequency))
	for term, frequency := range vocabularyFrequency {
		vocabulary = append(vocabulary, VocabularyTerm{Term: term, DocumentFrequency: frequency})
	}
	sort.Slice(vocabulary, func(i, j int) bool { return vocabulary[i].Term < vocabulary[j].Term })
	expansions := SelectFuzzyExpansions(plan, vocabulary)
	for _, expansion := range expansions {
		documentFrequency[expansion.DocumentTerm] = expansion.DocumentFrequency
	}
	return documentFrequency, expansions, warnings, nil
}

func fuzzySimilarity(queryLength, candidateLength, distance int) int {
	maximum := queryLength
	if candidateLength > maximum {
		maximum = candidateLength
	}
	if maximum == 0 {
		return 0
	}
	return (maximum - distance) * 1000 / maximum
}

func boundedDamerauLevenshtein(left, right string, maximum int) int {
	leftLength := utf8.RuneCountInString(left)
	rightLength := utf8.RuneCountInString(right)
	if absolute(leftLength-rightLength) > maximum {
		return maximum + 1
	}
	const maximumComparedRunes = MaximumFuzzyTokenRunes + 2
	if leftLength > maximumComparedRunes || rightLength > maximumComparedRunes {
		return maximum + 1
	}
	var leftRunes, rightRunes [maximumComparedRunes]rune
	index := 0
	for _, value := range left {
		leftRunes[index] = value
		index++
	}
	index = 0
	for _, value := range right {
		rightRunes[index] = value
		index++
	}
	var previousPrevious, previous, current [maximumComparedRunes + 1]int
	for index := 0; index <= rightLength; index++ {
		previous[index] = index
	}
	for i := 1; i <= leftLength; i++ {
		current[0] = i
		for j := 1; j <= rightLength; j++ {
			cost := 0
			if leftRunes[i-1] != rightRunes[j-1] {
				cost = 1
			}
			current[j] = minimum(
				previous[j]+1,
				current[j-1]+1,
				previous[j-1]+cost,
			)
			if i > 1 && j > 1 && leftRunes[i-1] == rightRunes[j-2] && leftRunes[i-2] == rightRunes[j-1] {
				transposition := previousPrevious[j-2] + 1
				if transposition < current[j] {
					current[j] = transposition
				}
			}
		}
		previousPrevious, previous, current = previous, current, previousPrevious
	}
	return previous[rightLength]
}

func minimum(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func absolute(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
