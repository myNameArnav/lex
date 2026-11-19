//go:build unix

package proc

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

// A job that keeps reporting progress may run past the stall limit; one
// that goes quiet is killed.
func TestRunWatched(t *testing.T) {
	// Generous gaps: the job runs at the lowest priority, and on a busy
	// machine (CI runs the ffmpeg tests alongside) it can wait a while
	// for the CPU. It still runs well past the stall limit in total.
	slow := exec.Command("sh", "-c", `for i in 1 2 3 4 5 6; do echo "out_time=$i" >&3; sleep 0.3; done`)
	if err := RunWatched(slow, time.Second); err != nil {
		t.Fatalf("progressing job: %v", err)
	}
	start := time.Now()
	stuck := exec.Command("sh", "-c", `echo progress=continue >&3; sleep 30`)
	if err := RunWatched(stuck, time.Second); !errors.Is(err, ErrStalled) {
		t.Fatalf("stuck job: %v, want ErrStalled", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("stuck job took %s to stop", d)
	}
	if err := RunWatched(exec.Command("sh", "-c", "exit 3"), time.Second); err == nil {
		t.Fatal("failing job: no error")
	}
}

func TestAcquireDiskOneAtATime(t *testing.T) {
	release, err := AcquireDisk(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := AcquireDisk(ctx); err == nil {
		t.Fatal("second job got the slot while the first held it")
	}
	release()
	r2, err := AcquireDisk(context.Background())
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	r2()
}
