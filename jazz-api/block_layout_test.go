package main

import (
	"github.com/google/uuid"
	"testing"
)

func TestBlockLayoutRequiresExactMembership(t *testing.T) {
	a, b, other := uuid.New(), uuid.New(), uuid.New()
	for _, test := range []struct {
		name    string
		input   blockLayoutRequest
		current []uuid.UUID
		valid   bool
	}{
		{"reorder", blockLayoutRequest{BlockIDs: []uuid.UUID{b, a}}, []uuid.UUID{a, b}, true},
		{"explicit removal", blockLayoutRequest{BlockIDs: []uuid.UUID{b}, RemoveID: &a}, []uuid.UUID{a, b}, true},
		{"remove last", blockLayoutRequest{BlockIDs: []uuid.UUID{}, RemoveID: &a}, []uuid.UUID{a}, true},
		{"missing concurrently added block", blockLayoutRequest{BlockIDs: []uuid.UUID{a}}, []uuid.UUID{a, b}, false},
		{"foreign block", blockLayoutRequest{BlockIDs: []uuid.UUID{a, other}}, []uuid.UUID{a, b}, false},
		{"duplicate", blockLayoutRequest{BlockIDs: []uuid.UUID{a, a}}, []uuid.UUID{a, b}, false},
		{"removed block still listed", blockLayoutRequest{BlockIDs: []uuid.UUID{a, b}, RemoveID: &a}, []uuid.UUID{a, b}, false},
		{"foreign removal", blockLayoutRequest{BlockIDs: []uuid.UUID{a}, RemoveID: &other}, []uuid.UUID{a, b}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validBlockLayout(test.input, test.current); got != test.valid {
				t.Fatalf("got %v, want %v", got, test.valid)
			}
		})
	}
}
