package services_test

import (
	"context"
	"time"

	"github.com/smartcontractkit/chainlink/core/services"
)

type example struct {
	services.ServiceCtx
	work chan struct{}
	g    *services.Group
}

func (e *example) start(ctx context.Context) error {
	e.g.Go(func() {
		for {
			select {
			case <-g.Stopped():
			case <-time.After(time.Minute):
				e.doWork()
			}
		}
	})
	return nil
}

func (e *example) doWork() {
	select {
	case w, ok := <-e.work:

	default:

	}
}

func NewExample() services.ServiceCtx {
	e := &example{
		work: make(chan struct{}),
	}
	e.ServiceCtx, e.g = services.New(services.Spec{
		Name:        "Example",
		Start:       e.start,
		SubServices: []services.ServiceCtx{
			//TODO
		},
	})
	return e
}

func Example() {

}
