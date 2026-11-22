package proc

import (
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"testing"
)

func TestArgsOfAnotherProcess(t *testing.T) {
	if os.Getenv("AINTERLINKED_ARGS_CHILD") != "" {
		_, _ = os.Stdin.Read(make([]byte, 1)) // until the test is done
		return
	}
	want := []string{"-test.run=TestArgsOfAnotherProcess", "-p", "two words", `quo"ted`}
	cmd := exec.Command(os.Args[0], want...)
	cmd.Env = append(os.Environ(), "AINTERLINKED_ARGS_CHILD=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
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
