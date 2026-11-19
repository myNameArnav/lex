package proc

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

// diskSlot lets one background job at a time read a media file from start
// to end (trickplay thumbnails, intro fingerprints). On a hard drive two
// such readers make it seek between files, so both take far longer than
// running one after the other, and both compete with playback for longer.
var diskSlot = make(chan struct{}, 1)

// AcquireDisk waits for the background disk slot. Call release when the
// read is done.
func AcquireDisk(ctx context.Context) (release func(), err error) {
	select {
	case diskSlot <- struct{}{}:
		return func() { <-diskSlot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ErrStalled is returned by RunWatched when ffmpeg stopped making progress.
var ErrStalled = errors.New("no progress")

// ProgressArgs make ffmpeg report progress on file descriptor 3, for
// RunWatched. They go before the inputs.
var ProgressArgs = []string{"-nostats", "-progress", "pipe:3", "-stats_period", "5"}

// RunWatched starts cmd (an ffmpeg with ProgressArgs) at the lowest
// priority and waits for it, killing it if no progress line arrives for
// stall. A slow disk then only makes the job take longer; a stuck one
// still ends. The command's context sets the hard limit.
func RunWatched(cmd *exec.Cmd, stall time.Duration) error {
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, pw)
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return err
	}
	pw.Close()
	Nice(cmd.Process.Pid, 19)
	progress := make(chan struct{}, 1)
	go func() {
		defer pr.Close()
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			select {
			case progress <- struct{}{}:
			default:
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t := time.NewTimer(stall)
	defer t.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-progress:
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
			t.Reset(stall)
		case <-t.C:
			cmd.Process.Kill()
			<-done
			return ErrStalled
		}
	}
}
