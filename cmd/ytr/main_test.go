package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCLIProcessUsesExpectedStreamsAndExitStatus(t *testing.T) {
	for _, tc := range []struct {
		args   string
		code   int
		output string
	}{{"help GetIssue", 0, "issueID"}, {"Unknown", 2, "unknown operation"}, {"GetCurrentUser", 2, "YTR_TOKEN is required"}} {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCLIHelperProcess$")
		cmd.Env = append(os.Environ(), "YTR_CLI_HELPER=1", "YTR_CLI_TEST_ARGS="+tc.args, "YTR_TOKEN=")
		output, err := cmd.CombinedOutput()
		code := 0
		failure := &exec.ExitError{}
		if errors.As(err, &failure) {
			code = failure.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != tc.code || !strings.Contains(string(output), tc.output) {
			t.Fatalf("%s: exit %d, output %s", tc.args, code, output)
		}
	}
}

func TestCLIProcessDeadlineUnblocksStdin(t *testing.T) {
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	var diagnostics bytes.Buffer
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCLIHelperProcess$")
	cmd.Env = append(os.Environ(), "YTR_CLI_HELPER=1", "YTR_CLI_TEST_ARGS=-timeout 100ms -data - SearchIssues", "YTR_TOKEN=fixture-token", "YTR_ORG_ID=fixture", "YTR_ORG_TYPE=360", "YTR_AUTH_SCHEME=OAuth")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, io.Discard, &diagnostics
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var failure *exec.ExitError
		if !errors.As(err, &failure) || failure.ExitCode() != 1 || !strings.Contains(diagnostics.String(), "context deadline exceeded") {
			t.Fatalf("stdin deadline: error=%v diagnostics=%q", err, diagnostics.String())
		}
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("CLI process remained blocked on stdin past its deadline")
	}
}

func TestCLIHelperProcess(_ *testing.T) {
	if os.Getenv("YTR_CLI_HELPER") != "1" {
		return
	}
	os.Args = append([]string{"ytr"}, strings.Fields(os.Getenv("YTR_CLI_TEST_ARGS"))...)
	main()
	os.Exit(0)
}
