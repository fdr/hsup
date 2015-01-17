package hsup

import (
	"errors"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"
)

var ErrNoSlugURL = errors.New("no slug specified")

const ProfileRunnerText = `#!/bin/bash
export PS1='\[\033[01;34m\]\w\[\033[00m\] \[\033[01;32m\]$ \[\033[00m\]'

if [ -d /etc/profile.d ]; then
  for i in /etc/profile.d/*.sh; do
    if [ -r $i ]; then
      . $i
    fi
  done
  unset i
fi

if [ -d /app/.profile.d ]; then
  for i in /app/.profile.d/*.sh; do
    if [ -r $i ]; then
      . $i
    fi
  done
  unset i
fi

rm $0
exec "$@"
`

type ProfileRunner struct {
	file *os.File
}

func (pr *ProfileRunner) Init() (err error) {
	if pr.file, err = ioutil.TempFile("", "pr_"); err != nil {
		return err
	}

	if _, err = pr.file.Write([]byte(ProfileRunnerText)); err != nil {
		return err
	}

	fi, err := os.Stat(pr.file.Name())
	if err != nil {
		return err
	}

	if err = os.Chmod(pr.file.Name(), fi.Mode()|0111); err != nil {
		return err
	}

	return pr.file.Close()
}

func (pr *ProfileRunner) Args(args []string) []string {
	return append([]string{pr.file.Name()}, args...)
}

type AbsPathDynoDriver struct{}

func (dd *AbsPathDynoDriver) fetch(release *Release) error {
	if release.slugURL == "" {
		return ErrNoSlugURL
	}

	log.Printf("fetching slug URL %q", release.slugURL)

	resp, err := http.Get(release.slugURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create("/tmp/slug.tgz")
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return err
	}

	return nil
}

func (dd *AbsPathDynoDriver) unpack(release *Release) error {
	if release.slugURL == "" {
		return nil
	}

	cmd := exec.Command("tar", "-C", "/app", "--strip-components=2", "-zxf",
		"/tmp/slug.tgz")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	return nil
}

func (dd *AbsPathDynoDriver) Build(release *Release) (err error) {
	if err = dd.fetch(release); err != nil {
		return err
	}

	if err = dd.unpack(release); err != nil {
		return err
	}

	return nil
}

func (dd *AbsPathDynoDriver) Start(ex *Executor) (err error) {
	var pr ProfileRunner
	if err = pr.Init(); err != nil {
		return err
	}

	args := pr.Args(ex.args)
	ex.cmd = exec.Command(args[0], args[1:]...)

	ex.cmd.Stdin = os.Stdin
	ex.cmd.Stdout = os.Stdout
	ex.cmd.Stderr = os.Stderr
	ex.cmd.Dir = "/app"

	// Fill environment vector from Heroku configuration, with a
	// default $PATH.
	ex.cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:" +
		"/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/app", "DYNO=" + ex.Name(), "PORT=5000"}

	for k, v := range ex.release.config {
		ex.cmd.Env = append(ex.cmd.Env, k+"="+v)
	}

	ex.cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err = ex.cmd.Start(); err != nil {
		return err
	}

	ex.waiting = make(chan struct{})
	return nil
}

func (dd *AbsPathDynoDriver) Wait(ex *Executor) (s *ExitStatus) {
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

func (dd *AbsPathDynoDriver) Stop(ex *Executor) error {
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
