package scenario_test

import (
	"fmt"

	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/scenario"
)

// Build a deterministic impairment engine from a named example scenario and run
// packets through it in-process — no sockets, no real clock. The same seed and
// the same packets always produce the same forward/drop decisions, so a run is
// reproducible: re-build the engine and the impairment schedule is identical.
func Example() {
	build := func() *engine.Engine {
		eng, err := scenario.Build(scenario.Examples()["lossy-burst"])
		if err != nil {
			panic(err)
		}
		return eng
	}
	// Push n client->server packets through, one per virtual millisecond, and
	// tally how many the engine forwards vs drops.
	run := func(eng *engine.Engine, n int) (forwarded, dropped int) {
		for i := 0; i < n; i++ {
			for _, a := range eng.Handle(engine.Packet{Data: []byte("frame"), Dir: engine.C2S}, int64(i)*1_000_000) {
				if a.Kind == engine.Drop {
					dropped++
				} else {
					forwarded++
				}
			}
		}
		return
	}

	f1, d1 := run(build(), 500)
	f2, d2 := run(build(), 500)

	fmt.Println("reproducible:", f1 == f2 && d1 == d2)
	fmt.Println("loss induced:", d1 > 0)
	fmt.Println("accounted:", f1+d1)
	// Output:
	// reproducible: true
	// loss induced: true
	// accounted: 500
}
