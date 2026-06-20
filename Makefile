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
# pattern. Fails the build on any non-deterministic regression (P0.7). The bond
# golden adds the multi-link dimension (P2.1) — two independent BuildLink paths —
# and the provable 2022-7 masking proof (P2.6).
schedule-golden:
	go test ./scenario/ -run 'TestScheduleGolden|TestDeterministicAcrossRuns' -count=1
	go test ./bond/ -run 'TestBondGolden|TestBondDeterministic|TestBondMaskingProvable|TestBondIndependence|TestBondNegativeControl' -count=1
	go test ./fec/ -run 'TestFECGolden|TestRecover|TestRecoverOrderIndependent|TestFECFromDroplist|TestOracleNegativeControls' -count=1

# Regenerate the committed goldens after an intentional behavior change.
update-golden:
	UPDATE_GOLDEN=1 go test ./scenario/ -run TestScheduleGolden -count=1
	UPDATE_GOLDEN=1 go test ./bond/ -run TestBondGolden -count=1
	UPDATE_GOLDEN=1 go test ./fec/ -run TestFECGolden -count=1
