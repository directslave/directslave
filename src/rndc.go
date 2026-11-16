package main

import (
	"os/exec"
	"strings"
	"sync"
	"time"
)

func startRndcProcessor(c chan int) {
	LogAction.Println("Statrting RNDC processor... OK")
	for {
		time.Sleep(time.Second * 30)
		i := 0
		for lenC := len(c); lenC > 0; lenC-- {
			// удаляем ивент из очереди
			_ = <-c
			i++
		}
		if i > 0 {
			LogAction.Println("RNDC queue triggered with", i, "events")
			runRndc()
		}
	}
}

func useRndc() {
	// Integrated DNS is updated directly and must never touch the legacy RNDC queue.
	if integratedDNSEnabled() {
		return
	}
	if Config.rndc_path != "none" && rchan != nil {
		rchan <- 1
	}
}

func runRndc() {
	runRndcCommand("reload")
	return
}

// runRndcCommand runs one rndc subcommand and waits for it. Arguments reach
// exec as a slice, never through a shell, so a zone name cannot be interpreted.
func runRndcCommand(args ...string) {

	if Config.rndc_path == "none" {
		return
	}

	wg := new(sync.WaitGroup)

	wg.Add(1)

	go rndcReloadExec(strings.Fields(Config.rndc_path+" "+strings.Join(args, " ")), wg)

	wg.Wait()

	return
}

func rndcReloadExec(parts []string, wg *sync.WaitGroup) {

	defer wg.Done()

	if len(parts) == 0 {
		return
	}

	head := parts[0]
	parts = parts[1:]

	out, err := exec.Command(head, parts...).Output()
	if err != nil {
		LogError.Printf("%s", err)
	}

	LogAction.Printf("RNDC output: %s", out)
}
