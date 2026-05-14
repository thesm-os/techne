// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package lang

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Mock runner helpers ---

type mockRunner struct {
	name    string
	report  SuiteReport
	patches []SuggestedPatch
	err     error
	called  *bool
}

func (m *mockRunner) Name() string { return m.name }

func (m *mockRunner) Run(_ context.Context, _ VerifyInput, _ string) (SuiteReport, []SuggestedPatch, error) {
	if m.called != nil {
		*m.called = true
	}
	return m.report, m.patches, m.err
}

func newMock(name, status string) *mockRunner {
	called := false
	return &mockRunner{
		name:   name,
		report: SuiteReport{Status: status, Summary: name + " ran"},
		called: &called,
	}
}

func withIssues(m *mockRunner, issues ...Issue) *mockRunner {
	m.report.Issues = issues
	return m
}

func withPatches(m *mockRunner, patches ...SuggestedPatch) *mockRunner {
	m.patches = patches
	return m
}

// --- Tests ---

func TestExecute_CallsRequestedRunners(t *testing.T) {
	lint := newMock(SuiteLint, StatusPass)
	test := newMock(SuiteTest, StatusPass)
	bench := newMock(SuiteBench, StatusPass)

	engine := NewEngine(lint, test, bench)
	input := VerifyInput{Suites: []string{SuiteLint, SuiteTest}}

	out, err := engine.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !*lint.called {
		t.Error("expected lint runner to be called")
	}
	if !*test.called {
		t.Error("expected test runner to be called")
	}
	if *bench.called {
		t.Error("expected bench runner NOT to be called")
	}

	if _, ok := out.Reports[SuiteLint]; !ok {
		t.Error("expected lint report in output")
	}
	if _, ok := out.Reports[SuiteTest]; !ok {
		t.Error("expected test report in output")
	}
	if _, ok := out.Reports[SuiteBench]; ok {
		t.Error("expected no bench report in output")
	}
}

func TestExecute_UnknownSuiteSilentlySkipped(t *testing.T) {
	lint := newMock(SuiteLint, StatusPass)
	engine := NewEngine(lint)
	input := VerifyInput{Suites: []string{SuiteLint, "does-not-exist"}}

	out, err := engine.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := out.Reports["does-not-exist"]; ok {
		t.Error("unknown suite should not appear in reports")
	}
	if out.OverallStatus != StatusPass {
		t.Errorf("expected overall pass, got %q", out.OverallStatus)
	}
}

func TestExecute_FailFastStopsAfterFirstFailure(t *testing.T) {
	lint := newMock(SuiteLint, StatusFail)
	test := newMock(SuiteTest, StatusPass)

	engine := NewEngine(lint, test)
	input := VerifyInput{
		Suites:   []string{SuiteLint, SuiteTest},
		FailFast: true,
	}

	out, err := engine.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if *test.called {
		t.Error("test runner should not have been called due to FailFast")
	}
	if _, ok := out.Reports[SuiteTest]; ok {
		t.Error("test report should not exist when FailFast triggered")
	}
	if out.OverallStatus != StatusDegraded {
		t.Errorf("expected degraded, got %q", out.OverallStatus)
	}
}

func TestExecute_MaxIssuesPerTypeTruncates(t *testing.T) {
	// Use unique messages so issues aren't clustered.
	issues := make([]Issue, 10)
	for i := range issues {
		issues[i] = Issue{File: "foo.go", Line: i + 1, Message: fmt.Sprintf("issue %d", i)}
	}

	lint := withIssues(newMock(SuiteLint, StatusFail), issues...)
	engine := NewEngine(lint)
	input := VerifyInput{
		Suites:           []string{SuiteLint},
		MaxIssuesPerType: 3,
	}

	out, err := engine.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	report := out.Reports[SuiteLint]
	if len(report.Issues) != 3 {
		t.Errorf("expected 3 issues after truncation, got %d", len(report.Issues))
	}
}

func TestExecute_DefaultMaxIssuesPerType(t *testing.T) {
	// Use unique messages so issues aren't clustered.
	issues := make([]Issue, 10)
	for i := range issues {
		issues[i] = Issue{File: "foo.go", Line: i + 1, Message: fmt.Sprintf("issue %d", i)}
	}

	lint := withIssues(newMock(SuiteLint, StatusFail), issues...)
	engine := NewEngine(lint)
	// MaxIssuesPerType=0 → default of 5.
	input := VerifyInput{Suites: []string{SuiteLint}}

	out, err := engine.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	report := out.Reports[SuiteLint]
	if len(report.Issues) != defaultMaxIssuesPerType {
		t.Errorf("expected %d issues (default), got %d", defaultMaxIssuesPerType, len(report.Issues))
	}
}

func TestExecute_ClustersDuplicateIssues(t *testing.T) {
	// 10 issues with the same message → should be clustered into 1 cluster.
	issues := make([]Issue, 10)
	for i := range issues {
		issues[i] = Issue{File: "foo.go", Line: i + 1, Linter: "errcheck", Message: "unchecked error"}
	}

	lint := withIssues(newMock(SuiteLint, StatusFail), issues...)
	engine := NewEngine(lint)
	input := VerifyInput{Suites: []string{SuiteLint}}

	out, err := engine.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	report := out.Reports[SuiteLint]
	if len(report.Issues) != 0 {
		t.Errorf("expected 0 unique issues (all clustered), got %d", len(report.Issues))
	}
	if len(report.Clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(report.Clusters))
	}
	cluster := report.Clusters[0]
	if cluster.Count != 5 { // 10 issues truncated to default 5, then clustered
		t.Errorf("expected cluster count 5, got %d", cluster.Count)
	}
	if len(cluster.Examples) > 3 {
		t.Errorf("expected at most 3 examples, got %d", len(cluster.Examples))
	}
}

func TestExecute_OverallStatusRollup(t *testing.T) {
	cases := []struct {
		suiteStatuses []string
		want          string
	}{
		{[]string{StatusPass, StatusPass}, StatusPass},
		{[]string{StatusPass, StatusFail}, StatusDegraded},
		{[]string{StatusPass, StatusError}, StatusDegraded},
		{[]string{StatusFail, StatusError}, StatusDegraded},
	}

	for _, tc := range cases {
		var runners []SuiteRunner
		suites := make([]string, len(tc.suiteStatuses))
		for i, status := range tc.suiteStatuses {
			name := "suite" + string(rune('A'+i))
			runners = append(runners, newMock(name, status))
			suites[i] = name
		}

		engine := NewEngine(runners...)
		input := VerifyInput{Suites: suites}

		out, err := engine.Execute(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out.OverallStatus != tc.want {
			t.Errorf("statuses %v: expected %q, got %q", tc.suiteStatuses, tc.want, out.OverallStatus)
		}
	}
}

func TestExecute_SuggestedPatchesCollectedFromAllRunners(t *testing.T) {
	patchA := SuggestedPatch{FilePath: "a.go", Reason: "fmt", Source: "gofmt"}
	patchB := SuggestedPatch{FilePath: "b.go", Reason: SuiteLint, Source: "golangci-lint"}

	lint := withPatches(newMock(SuiteLint, StatusPass), patchA)
	test := withPatches(newMock(SuiteTest, StatusPass), patchB)

	engine := NewEngine(lint, test)
	input := VerifyInput{Suites: []string{SuiteLint, SuiteTest}}

	out, err := engine.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(out.SuggestedPatches) != 2 {
		t.Errorf("expected 2 patches, got %d", len(out.SuggestedPatches))
	}
}

func TestExecute_NextActionsEmptyByDefault(t *testing.T) {
	lint := newMock(SuiteLint, StatusPass)
	engine := NewEngine(lint)
	input := VerifyInput{Suites: []string{SuiteLint}}

	out, err := engine.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(out.NextActions) != 0 {
		t.Errorf(
			"Engine should not generate NextActions — language-specific tool adds them; got %d",
			len(out.NextActions),
		)
	}
}

func TestExecute_LogIDGeneratedAndDirectoryCreated(t *testing.T) {
	engine := NewEngine(newMock(SuiteLint, StatusPass))
	input := VerifyInput{Suites: []string{SuiteLint}}

	out, err := engine.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logID := out.BloatPrevention.LogID
	if logID == "" {
		t.Fatal("expected non-empty log ID")
	}
	if !strings.HasPrefix(logID, "et-") {
		t.Errorf("expected log ID to start with 'et-', got %q", logID)
	}
	if len(logID) != 11 { // "et-" + 8 hex chars
		t.Errorf("expected log ID length 11, got %d (%q)", len(logID), logID)
	}

	logDir := filepath.Join(logBaseDir, logID)
	if _, statErr := os.Stat(logDir); errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("expected log directory %q to exist", logDir)
	}

	// Cleanup.
	os.RemoveAll(logDir)
}

func TestExecute_BloatPreventionPopulated(t *testing.T) {
	engine := NewEngine(newMock(SuiteLint, StatusPass))
	input := VerifyInput{Suites: []string{SuiteLint}}

	out, err := engine.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	bp := out.BloatPrevention
	if bp.LogID == "" {
		t.Error("BloatPrevention.LogID should be set")
	}
	if bp.Hint == "" {
		t.Error("BloatPrevention.Hint should be set")
	}

	// Cleanup.
	os.RemoveAll(filepath.Join(logBaseDir, bp.LogID))
}

func TestExecute_RunnerErrorSetsStatusError(t *testing.T) {
	broken := &mockRunner{
		name:   SuiteLint,
		report: SuiteReport{},
		err:    errors.New("tool crashed"),
		called: func() *bool { b := false; return &b }(),
	}

	engine := NewEngine(broken)
	input := VerifyInput{Suites: []string{SuiteLint}}

	out, err := engine.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	report := out.Reports[SuiteLint]
	if report.Status != StatusError {
		t.Errorf("expected status 'error', got %q", report.Status)
	}
	if out.OverallStatus != StatusDegraded {
		t.Errorf("expected overall 'degraded', got %q", out.OverallStatus)
	}

	// Cleanup.
	os.RemoveAll(filepath.Join(logBaseDir, out.BloatPrevention.LogID))
}
