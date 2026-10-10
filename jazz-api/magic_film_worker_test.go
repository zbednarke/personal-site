package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

func TestValidMagicTarget(t *testing.T) {
	for _, value := range []int{60, 120, 180, 300, 600} {
		if !validMagicTarget(value) {
			t.Fatalf("expected %d to be valid", value)
		}
	}
	for _, value := range []int{0, 59, 90, 601} {
		if validMagicTarget(value) {
			t.Fatalf("expected %d to be invalid", value)
		}
	}
}

func testMagicManifest() magicFilmManifest {
	return magicFilmManifest{TargetSeconds: 60, Candidates: []magicFilmCandidate{
		{ID: "manual", RecordingID: "take-1", StartMS: 1000, EndMS: 31000, Manual: true},
		{ID: "other", RecordingID: "take-1", StartMS: 40000, EndMS: 100000},
	}}
}

func TestValidateMagicFilmPlan(t *testing.T) {
	manifest := testMagicManifest()
	valid := magicFilmPlan{
		Title:   "Practice film",
		Reviews: []magicFilmReview{{CandidateID: "manual", Reason: "Strong user selection"}},
		Clips:   []magicFilmCut{{CandidateID: "manual", StartMS: 1000, EndMS: 26000}},
	}
	if duration, err := validateMagicFilmPlan(valid, manifest); err != nil || duration != 25000 {
		t.Fatalf("valid plan: duration=%d err=%v", duration, err)
	}

	tests := []struct {
		name string
		edit func(*magicFilmPlan)
		want string
	}{
		{"missing priority review", func(plan *magicFilmPlan) { plan.Reviews = nil }, "did not review"},
		{"outside candidate", func(plan *magicFilmPlan) { plan.Clips[0].StartMS = 0 }, "outside a candidate"},
		{"omits priority", func(plan *magicFilmPlan) {
			plan.Clips = []magicFilmCut{{CandidateID: "other", StartMS: 40000, EndMS: 50000}}
		}, "omitted every"},
		{"overlap", func(plan *magicFilmPlan) {
			plan.Clips = append(plan.Clips, magicFilmCut{CandidateID: "manual", StartMS: 25000, EndMS: 30000})
		}, "overlapping"},
		{"too long", func(plan *magicFilmPlan) {
			plan.Clips = []magicFilmCut{{CandidateID: "manual", StartMS: 1000, EndMS: 31000}, {CandidateID: "other", StartMS: 40000, EndMS: 100000}}
		}, "exceeds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := valid
			plan.Reviews = append([]magicFilmReview(nil), valid.Reviews...)
			plan.Clips = append([]magicFilmCut(nil), valid.Clips...)
			tt.edit(&plan)
			if _, err := validateMagicFilmPlan(plan, manifest); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestInvokeMagicFilmUsesOneTaskOverride(t *testing.T) {
	id := uuid.New()
	app := &application{
		cfg:         config{MagicFilmProject: "project", MagicFilmRegion: "region", MagicFilmJob: "worker"},
		tokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "token"}),
	}
	app.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v2/projects/project/locations/region/jobs/worker:run" {
			t.Fatalf("unexpected path %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer token" {
			t.Fatal("missing API bearer token")
		}
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), `"taskCount":1`) || !strings.Contains(string(body), id.String()) {
			t.Fatalf("unexpected override body %s", body)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"name":"operations/run-1"}`)), Header: make(http.Header)}, nil
	})}
	ref, err := app.invokeMagicFilm(context.Background(), id)
	if err != nil || ref != "operations/run-1" {
		t.Fatalf("ref=%q err=%v", ref, err)
	}
}
