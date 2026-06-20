.PHONY: build test race vet tidy schedule-golden

build:
	go build ./...

test:
	go test ./...

race:
	go test -race -count=1 ./...

vet:
	go vet ./...

tidy:
	go mod tidy

# Determinism gate: same seed + fixed trace must reproduce a byte-identical
# pattern. Fails the build on any non-deterministic regression (P0.7).
schedule-golden:
	go test ./internal/scenario/ -run 'TestScheduleGolden|TestDeterministicAcrossRuns' -count=1

# Regenerate the committed golden after an intentional behavior change.
update-golden:
	UPDATE_GOLDEN=1 go test ./internal/scenario/ -run TestScheduleGolden -count=1
