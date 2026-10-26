package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Only these curated fields are sent to the browser. Historical jobs can
// contain provider responses or signed media URLs in their stored error.
type magicFilmFailure struct {
	Code      string `json:"code"`
	Title     string `json:"title"`
	Message   string `json:"message"`
	Action    string `json:"action"`
	Retryable bool   `json:"retryable"`
}

func magicFilmProviderFailure(status int, body []byte) error {
	var response struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &response)
	// Preserve only recognized identifiers, never arbitrary provider text.
	kind := "provider_error"
	for _, value := range []string{response.Error.Code, response.Error.Type} {
		if value == "credit_balance_exhausted" || value == "insufficient_quota" {
			kind = "insufficient_quota"
		}
	}
	return fmt.Errorf("Magic Film provider HTTP %d: %s", status, kind)
}

func describeMagicFilmFailure(raw, message string) *magicFilmFailure {
	f := &magicFilmFailure{"unknown", "Film could not finish", "Something went wrong while making this film.", "Try again once. If it fails again, use the job reference to investigate.", true}
	// Recognize both the current safe error format and previously stored errors.
	var status int
	if _, err := fmt.Sscanf(raw, "Magic Film provider HTTP %d:", &status); err != nil {
		_, _ = fmt.Sscanf(raw, "Astra returned %d:", &status)
	}
	providerError := raw
	if status != 0 && strings.HasPrefix(raw, "Astra returned ") {
		if _, body, ok := strings.Cut(raw, ":"); ok {
			providerError = magicFilmProviderFailure(status, []byte(body)).Error()
		}
	}
	switch {
	case status != 0 && providerError == fmt.Sprintf("Magic Film provider HTTP %d: insufficient_quota", status):
		*f = magicFilmFailure{"provider_quota", "AI credits exhausted", "The film editor's API account has no available quota or credits. Retrying now will not help.", "Restore the film editor's AI access before trying again. Your ChatGPT subscription and this API account are billed separately.", false}
	case status == 429:
		*f = magicFilmFailure{"provider_rate_limit", "AI service is temporarily busy", "The AI provider limited this request. This is different from an exhausted credit balance.", "Wait a few minutes, then try again. This job will not retry automatically.", true}
	case status == 401 || status == 403:
		*f = magicFilmFailure{"provider_auth", "AI access needs attention", "The film worker could not authenticate or is not allowed to use the AI model.", "Fix the worker's provider credentials or model permissions, then try again. Signing in to this site again will not fix the worker's access.", false}
	case strings.Contains(raw, "OPENAI_API_KEY is not configured"):
		*f = magicFilmFailure{"provider_configuration", "AI editor is not connected", "The film worker has no configured AI credentials.", "Connect the film worker to a supported AI provider before trying again.", false}
	case status >= 500 && status <= 599:
		*f = magicFilmFailure{"provider_unavailable", "AI service is unavailable", "The AI provider could not complete the request.", "Wait a few minutes, then try again.", true}
	case strings.Contains(raw, "context deadline exceeded") || strings.Contains(raw, "Client.Timeout"):
		*f = magicFilmFailure{"timeout", "Film request timed out", "A step took too long to complete.", "Try again. If it keeps timing out, use the job reference to investigate.", true}
	case message == "Cloud worker could not start":
		*f = magicFilmFailure{"worker_start", "Film worker could not start", "The cloud worker did not confirm that it started.", "Check the film list before trying again. If this repeats, the worker configuration needs attention.", true}
	}
	return f
}

func (job *magicFilmJob) sanitizeFailure() {
	if job.Status == "failed" {
		job.Failure = describeMagicFilmFailure(job.Error, job.Message)
		// Keep older clients readable while withholding the raw stored error.
		job.Message = job.Failure.Title
		job.Error = job.Failure.Message + " " + job.Failure.Action
	} else {
		job.Error = ""
		job.Failure = nil
	}
}
