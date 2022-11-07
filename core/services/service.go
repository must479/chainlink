package services

import (
	"context"
	"fmt"
	"sync"

	"github.com/smartcontractkit/chainlink/core/logger"
	"github.com/smartcontractkit/chainlink/core/utils"
)

// ServiceCtx represents a long-running service inside the Application.
//
// Typically, a ServiceCtx will leverage utils.StartStopOnce to implement these
// calls in a safe manner, or bootstrap via New.
//
// # Template
//
// Mockable Foo service with a run loop
//
//	//go:generate mockery --quiet --name Foo --output ../internal/mocks/ --case=underscore
//	type (
//		// Expose a public interface so we can mock the service.
//		Foo interface {
//			service.ServiceCtx
//
//			// ...
//		}
//
//		foo struct {
//			// ...
//
//			stop chan struct{}
//			done chan struct{}
//
//			utils.StartStopOnce
//		}
//	)
//
//	var _ Foo = (*foo)(nil)
//
//	func NewFoo() Foo {
//		f := &foo{
//			// ...
//		}
//
//		return f
//	}
//
//	func (f *foo) Start(ctx context.Context) error {
//		return f.StartOnce("Foo", func() error {
//			go f.run()
//
//			return nil
//		})
//	}
//
//	func (f *foo) Close() error {
//		return f.StopOnce("Foo", func() error {
//			// trigger goroutine cleanup
//			close(f.stop)
//			// wait for cleanup to complete
//			<-f.done
//			return nil
//		})
//	}
//
//	func (f *foo) run() {
//		// signal cleanup completion
//		defer close(f.done)
//
//		for {
//			select {
//			// ...
//			case <-f.stop:
//				// stop the routine
//				return
//			}
//		}
//
//	}
type ServiceCtx interface {
	// Start the service. Must quit immediately if the context is cancelled.
	// The given context applies to Start function only and must not be retained.
	//
	// See MultiStart
	Start(context.Context) error
	// Close stops the Service.
	// Invariants: Usually after this call the Service cannot be started
	// again, you need to build a new Service to do so.
	//
	// See MultiClose
	Close() error

	Checkable
}

// Group tracks a group of goroutines and provides shutdown signals.
type Group struct {
	wg   sync.WaitGroup
	stop chan struct{}
	lggr logger.Logger
}

// Go calls fn in a tracked goroutine that will block closing the service.
// fn should yield to Closed() or CloseCtx() promptly.
func (g *Group) Go(fn func()) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		fn()
	}()
}

// Closed returns a channel that will be closed along with the service.
func (g *Group) Closed() <-chan struct{} {
	return g.stop
}

// CloseCtx returns a context.Context that will be canceled when the service is closed.
func (g *Group) CloseCtx() (context.Context, context.CancelFunc) {
	return utils.ContextFromChan(g.stop)
}

func (g *Group) Logger() logger.Logger { return g.lggr }

// Spec specifies a service.
type Spec struct {
	Name        string
	Start       func(context.Context, *Group) error
	SubServices []ServiceCtx
}

type service struct {
	utils.StartStopOnce
	g    Group
	spec Spec
}

// New returns a new ServiceCtx defined by Spec and a Group for managing goroutines and logging.
// You *should* embed the ServiceCtx (to inherit methods), but *not* the Group.
func New(spec Spec, lggr logger.Logger) (ServiceCtx, *Group) {
	s := &service{
		g: Group{
			stop: make(chan struct{}),
			lggr: lggr.Named(spec.Name),
		},
		spec: spec,
	}
	return s, &s.g
}

func (s *service) Start(ctx context.Context) error {
	return s.StartOnce(s.spec.Name, func() error {
		var ms MultiStart
		s.g.lggr.Debug("Starting sub-services")
		for _, sub := range s.spec.SubServices {
			if err := ms.Start(ctx, sub); err != nil {
				s.g.lggr.Errorw("Failed to start sub-service", "error", err)
				return fmt.Errorf("failed to start sub-service: %w", err)
			}
		}
		return s.spec.Start(ctx, &s.g)
	})
}

func (s *service) Close() error {
	return s.StopOnce(s.spec.Name, func() (err error) {
		s.g.lggr.Debug("Stopping sub-services")
		close(s.g.stop)
		defer s.g.wg.Wait()

		var mc MultiClose
		for _, sub := range s.spec.SubServices {
			mc = append(mc, sub)
		}
		return mc.Close()
	})
}
