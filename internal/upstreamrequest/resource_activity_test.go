package upstreamrequest

import (
	"context"
	"errors"
	"testing"
)

func TestBusinessResourcePriorityAndOverlappingLoads(t *testing.T) {
	c := NewChannel()
	ctx, finish, err := c.ResourceProbe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	end1, end2 := c.BeginResourceLoad(), c.BeginResourceLoad()
	if ctx.Err() != context.Canceled || !finish() {
		t.Fatal("business did not preempt probe")
	}
	end1()
	end1()
	if !c.ResourceBusy() {
		t.Fatal("overlapping business load lost")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.ResourceProbe(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	end2()
	ctx, finish, err = c.ResourceProbe(context.Background())
	if err != nil || ctx.Err() != nil || c.ResourceBusy() {
		t.Fatal("did not resume", err)
	}
	if finish() {
		t.Fatal("normal completion marked interrupted")
	}
}
