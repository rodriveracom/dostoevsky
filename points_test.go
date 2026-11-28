//go:build unix

package dostoevsky

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/rodriveracom/dostoevsky/internal/seam"
)

func arm(t *testing.T, name string) {
	t.Helper()
	if err := seam.Arm(name, false, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { seam.Disarm() })
}

func publishOne(t *testing.T, n *Node) {
	t.Helper()
	if _, err := n.Publish(context.Background(), p.Topic, Message{Value: []byte("x")}); err != nil {
		t.Fatal(err)
	}
}

// Every point the engine package owns is reached by the operation it is named for and fails it.
func TestEveryEnginePointIsReached(t *testing.T) {
	ctx := context.Background()
	publish := func(t *testing.T, n *Node, _ string) error {
		_, err := n.Publish(ctx, p.Topic, Message{Value: []byte("armed")})
		return err
	}
	retain := func(t *testing.T, n *Node, _ string) error { return n.Retain(ctx, map[Partition]int64{p: 1}) }
	backup := func(t *testing.T, n *Node, root string) error { return n.Backup(ctx, filepath.Join(root, "copy")) }
	for _, c := range []struct {
		point     string
		segmented bool
		act       func(*testing.T, *Node, string) error
		want      error
	}{
		{"journal.write", false, publish, seam.ErrInjected},
		{"journal.sync", false, publish, seam.ErrInjected},
		{"segment.write", true, publish, seam.ErrInjected},
		{"segment.sync", true, publish, seam.ErrInjected},
		{"range.publish", true, publish, seam.ErrInjected},
		{"dirsync", false, backup, seam.ErrInjected},
		{"retention.write", false, retain, seam.ErrInjected},
		{"retention.sync", false, retain, seam.ErrInjected},
		{"retention.rename", false, retain, seam.ErrInjected},
		{"retention.dirsync", false, retain, seam.ErrInjected},
		{"retention.truncate", false, retain, seam.ErrInjected},
		{"backup.write", false, backup, seam.ErrInjected},
		{"backup.rename", false, backup, seam.ErrInjected},
		{"backup.syncparent", false, backup, ErrBackupNotDurable},
	} {
		t.Run(c.point, func(t *testing.T) {
			root := t.TempDir()
			open := Open
			if c.segmented {
				open = OpenSegmented
			}
			n, err := open(filepath.Join(root, "store"), config())
			if err != nil {
				t.Fatal(err)
			}
			defer n.Close()
			publishOne(t, n)
			publishOne(t, n)
			if c.point == "range.publish" {
				n.rollBytes = 1 // the next publish starts a new segment
			}
			arm(t, c.point)
			err = c.act(t, n, root)
			if !seam.Disarm() {
				t.Fatalf("the point was not reached (the operation returned %v)", err)
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestAnUnknownPointIsRefusedAndAnUnreachedOneSaysSo(t *testing.T) {
	if err := seam.Arm("no.such.point", false, 0); err == nil {
		t.Fatal("an unknown name was armed")
	}
	n := openTest(t, t.TempDir(), config())
	defer n.Close()
	arm(t, "backup.write")
	publishOne(t, n)
	if seam.Disarm() {
		t.Fatal("a publish reached the backup point")
	}
	if err := seam.Arm("journal.write", false, 1); err != nil {
		t.Fatal(err)
	}
	publishOne(t, n) // the first time is skipped
	if _, err := n.Publish(context.Background(), p.Topic, Message{Value: []byte("y")}); !errors.Is(err, seam.ErrInjected) {
		t.Fatalf("the second time: %v", err)
	}
}

// A point armed to kill ends the process with SIGKILL, and what was acknowledged before is there afterwards.
func TestAPointArmedToKillKillsTheProcess(t *testing.T) {
	if dir := os.Getenv("DOST_POINT_KILL_DIR"); dir != "" {
		n := openTest(t, dir, config())
		publishOne(t, n)
		if err := seam.Arm("journal.sync", true, 0); err != nil {
			t.Fatal(err)
		}
		_, err := n.Publish(context.Background(), p.Topic, Message{Value: []byte("never acknowledged")})
		t.Fatalf("still running after the kill point: %v", err)
	}
	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestAPointArmedToKillKillsTheProcess$", "-test.count=1")
	child.Env = append(os.Environ(), "DOST_POINT_KILL_DIR="+dir)
	out, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("the child did not die: %v\n%s", err, out)
	}
	if status, ok := exit.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("the child ended otherwise: %v\n%s", err, out)
	}
	n := openTest(t, dir, config())
	defer n.Close()
	r, err := n.Read(context.Background(), p, 0, false)
	if err != nil || string(r.Value) != "x" {
		t.Fatalf("the acknowledged record: %q %v", r.Value, err)
	}
}
