package proc

import (
	"io"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"testing"
)

func TestArgsOfAnotherProcess(t *testing.T) {
	if os.Getenv("AINTERLINKED_ARGS_CHILD") != "" {
		_, _ = os.Stdout.Write([]byte("!"))   // started
		_, _ = os.Stdin.Read(make([]byte, 1)) // until the test is done
		return
	}
	// Past "--" the test binary doesn't parse them: an unknown flag ends it.
	want := []string{"-test.run=TestArgsOfAnotherProcess", "--", "-p", "two words", `quo"ted`}
	cmd := exec.Command(os.Args[0], want...)
	cmd.Env = append(os.Environ(), "AINTERLINKED_ARGS_CHILD=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(out, make([]byte, 1)); err != nil {
		t.Errorf("the child didn't start: %v", err)
	}
	got, err := Args(cmd.Process.Pid)
	in.Close()
	_ = cmd.Wait()
	if err != nil || len(got) < len(want) {
		t.Fatalf("%q %v", got, err)
	}
	if (runtime.GOOS == "windows" || runtime.GOOS == "linux") && !reflect.DeepEqual(got[1:], want) {
		t.Fatalf("got %q, want %q", got[1:], want)
	}
	if _, err := Args(1 << 30); err == nil {
		t.Fatal("no such process")
	}
}
