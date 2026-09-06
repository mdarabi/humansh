package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	probePrompt             = "Reply with exactly HUMANSH_READY and nothing else. Do not use tools or inspect external state."
	listRequest             = "list all the files in this directory"
	shortListRequest        = "list files"
	ambiguousRMRequest      = "rm is not working"
	findContentRequest      = `find the file with "ABC" content`
	createMarkerRequest     = "please create a marker file for me"
	deleteTargetRequest     = "please delete the e2e target directory"
	providerFailureRequest  = "show me a provider failure"
	privateEnvironmentValue = "HUMANSH_E2E_ENV_SECRET_DO_NOT_SEND"
	privateFileValue        = "HUMANSH_E2E_FILE_SECRET_DO_NOT_SEND"
)

type event struct {
	Event   string `json:"event"`
	Request string `json:"request"`
	PID     int    `json:"pid"`
}

type response struct {
	Status        string   `json:"status"`
	Command       string   `json:"command"`
	Explanation   string   `json:"explanation"`
	Clarification string   `json:"clarification"`
	Assumptions   []string `json:"assumptions"`
}

func main() {
	args := os.Args[1:]
	if len(args) == 2 && args[0] == "exec" && args[1] == probePrompt {
		recordProbe()
		fmt.Println("HUMANSH_READY")
		return
	}
	if len(args) == 0 || args[0] != "exec" {
		fail("unexpected Codex fixture invocation")
	}

	outputPath := ""
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--output-last-message" {
			outputPath = args[index+1]
			break
		}
	}
	if outputPath == "" || args[len(args)-1] != "-" {
		fail("translation invocation omitted its stdin or result channel")
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		fail("read translation request: %v", err)
	}
	if os.Getenv("HUMANSH_E2E_PRIVATE_ENV") != "" || bytes.Contains(input, []byte(privateEnvironmentValue)) || bytes.Contains(input, []byte(privateFileValue)) {
		fail("translation received private environment or file content")
	}

	request := requestInput(input)
	result, ok := fixtureResponse(request)
	if !ok {
		fail("translation fixture received an unexpected request: %q", request)
	}
	record("started", request)

	if request == findContentRequest {
		// The cancellation scenarios wait for the real provider subprocess to
		// start, then send a terminal key. Keep this process alive until Humansh
		// cancels its process group. The finite backstop prevents a broken test
		// harness from leaving the fixture alive indefinitely.
		time.Sleep(30 * time.Second)
	}
	if request == providerFailureRequest {
		record("failed", request)
		fail("fixture provider failure")
	}

	payload, err := json.Marshal(result)
	if err != nil {
		fail("encode translation response: %v", err)
	}
	if err := os.WriteFile(outputPath, payload, 0o600); err != nil {
		fail("write translation response: %v", err)
	}
	record("completed", request)
}

func recordProbe() {
	executable, err := os.Executable()
	if err != nil {
		fail("resolve fixture executable for setup probe: %v", err)
	}
	logPath := filepath.Join(filepath.Dir(executable), "humansh-e2e-probes.log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fail("open setup probe log: %v", err)
	}
	if _, err := fmt.Fprintln(file, "probe"); err != nil {
		_ = file.Close()
		fail("write setup probe log: %v", err)
	}
	if err := file.Close(); err != nil {
		fail("close setup probe log: %v", err)
	}
}

func requestInput(prompt []byte) string {
	const begin = "REQUEST_JSON_BEGIN\n"
	const end = "\nREQUEST_JSON_END"
	start := bytes.Index(prompt, []byte(begin))
	if start < 0 {
		fail("translation prompt omitted its request boundary")
	}
	start += len(begin)
	finish := bytes.Index(prompt[start:], []byte(end))
	if finish < 0 {
		fail("translation prompt omitted its closing request boundary")
	}
	var request struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal(prompt[start:start+finish], &request); err != nil {
		fail("decode translation request: %v", err)
	}
	return request.Input
}

func fixtureResponse(request string) (response, bool) {
	tests := []struct {
		request     string
		command     string
		explanation string
	}{
		{listRequest, "ls -la", "Lists every entry in the current directory."},
		{shortListRequest, "ls -la", "Lists every entry in the current directory."},
		{ambiguousRMRequest, "man rm", "Opens the manual for rm so its expected arguments can be checked."},
		{findContentRequest, "grep -R -- 'ABC' .", "Finds files below the current directory that contain ABC."},
		{createMarkerRequest, "touch humansh-e2e-generated-marker", "Creates the requested marker file."},
		{deleteTargetRequest, "rm -rf -- humansh-e2e-high-risk-target", "Recursively removes the requested test directory."},
		{providerFailureRequest, "", ""},
	}
	for _, test := range tests {
		if request == test.request {
			return response{
				Status:      "ok",
				Command:     test.command,
				Explanation: test.explanation,
				Assumptions: []string{},
			}, true
		}
	}
	return response{}, false
}

func record(eventName, request string) {
	executable, err := os.Executable()
	if err != nil {
		fail("resolve fixture executable: %v", err)
	}
	root := filepath.Dir(executable)
	if err := os.MkdirAll(root, 0o700); err != nil {
		fail("create fixture event directory: %v", err)
	}
	file, err := os.OpenFile(filepath.Join(root, "humansh-e2e-calls.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fail("open fixture event log: %v", err)
	}
	encodeErr := json.NewEncoder(file).Encode(event{Event: eventName, Request: request, PID: os.Getpid()})
	closeErr := file.Close()
	if encodeErr != nil || closeErr != nil {
		fail("write fixture event: %v %v", encodeErr, closeErr)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
