package main

import "testing"

func TestBackupObjectName(t *testing.T) {
	got := backupObjectName("users/user/date/take/video.webm")
	want := "users/user/date/take/video.original-unindexed.webm"
	if got != want {
		t.Fatalf("backupObjectName() = %q, want %q", got, want)
	}
}

func TestRepairedSizeIsSafe(t *testing.T) {
	const source = int64(700 << 20)
	if !repairedSizeIsSafe(source, source+(5<<20)) {
		t.Fatal("expected a sub-one-percent container metadata increase to be accepted")
	}
	if repairedSizeIsSafe(source, source-(100<<20)) {
		t.Fatal("expected a large loss of packet data to be rejected")
	}
}

func TestSmallRepairAllowsCueMetadata(t *testing.T) {
	const source = int64(35 << 20)
	if !repairedSizeIsSafe(source, source+(1<<20)) {
		t.Fatal("expected cue metadata overhead on a small recording to be accepted")
	}
}
