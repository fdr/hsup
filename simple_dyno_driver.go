package hsup

import (
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type SimpleDynoDriver struct {
}

func (dd *SimpleDynoDriver) Build(release *Release) error {
	return nil
}

func (dd *SimpleDynoDriver) Start(ex *Executor) error {
	ex.cmd = exec.Command(ex.args[0], ex.args[1:]...)

	ex.cmd.Stdin = os.Stdin
	ex.cmd.Stdout = os.Stdout
	ex.cmd.Stderr = os.Stderr

	// Fill environment vector from Heroku configuration.
	for k, v := range ex.release.config {
		ex.cmd.Env = append(ex.cmd.Env, k+"="+v)
	}

	ex.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	err := ex.cmd.Start()
	if err != nil {
		return err
	}

	ex.waiting = make(chan struct{})
	return nil
}

func (dd *SimpleDynoDriver) Wait(ex *Executor) (s *ExitStatus) {
	s = &ExitStatus{}
	err := ex.cmd.Wait()
	if err != nil {
		if eErr, ok := err.(*exec.ExitError); ok {
			if status, ok := eErr.Sys().(syscall.WaitStatus); ok {
				s.Code = status.ExitStatus()
			}
		} else {
			// Non ExitErrors are propagated: they are
			// liable to be errors in starting the
			// process.
			s.Err = err
		}
	}

	go func() {
		ex.waiting <- struct{}{}
	}()

	return s
}

func (dd *SimpleDynoDriver) Stop(ex *Executor) error {
	p := ex.cmd.Process

	group, err := os.FindProcess(-1 * p.Pid)
	if err != nil {
		return err
	}

	// Begin graceful shutdown via SIGTERM.
	group.Signal(syscall.SIGTERM)

	for {
		select {
		case <-time.After(10 * time.Second):
			log.Println("sigkill", group)
			group.Signal(syscall.SIGKILL)
		case <-ex.waiting:
			log.Println("waited", group)
			return nil
		}
		log.Println("spin", group)
		time.Sleep(1)
	}
}
