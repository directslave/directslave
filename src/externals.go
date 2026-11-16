package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
)

func RunExternalScript(role string, input map[string]string, script string) {
	execPath := "/usr/local/directslave/scripts/" + script
	binary, err := exec.LookPath(execPath)
	if err != nil {
		LogError.Printf("%s: script %s not found.", role, execPath)
		return
	}

	cmd := exec.Command(binary)
	env := os.Environ()

	for in := range input {
		env = append(env, fmt.Sprintf("%s=%s", in, input[in]))
	}

	cmd.Env = env

	cmdOut, _ := cmd.StdoutPipe()

	startErr := cmd.Start()
	if startErr != nil {
		LogError.Printf("%s: script %s not found.", role, execPath)
		return
	}

	// read stdout and stderr
	stdOutput, _ := ioutil.ReadAll(cmdOut)

	if Config.debug == "1" {
		LogError.Printf("%s: %s output : %s\n", role, execPath, stdOutput)
	}

	_ = cmd.Wait()

	LogAction.Printf("%s: %s executed.\n", role, execPath)

	return
}
