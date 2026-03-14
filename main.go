package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"kubjas/internal/config"
	"kubjas/internal/scheduler"
)

var (
	confFile = flag.String("conf_file", "/etc/kubjas.conf", "main configuration file")
	confDir  = flag.String("conf_dir", "/etc/kubjas.d", "configuration directory")
	logFile  = flag.String("log_file", "", "log file path (default: stdout)")
	pidFile  = flag.String("pid_file", "", "PID file path")
)

func main() {
	flag.Parse()

	// Resolve relative conf_file path
	if !filepath.IsAbs(*confFile) {
		cwd, _ := os.Getwd()
		*confFile = filepath.Join(cwd, *confFile)
	}

	// Log file redirect
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatalf("cannot open log file %s: %v", *logFile, err)
		}
		log.SetOutput(f)
		os.Stdout = f
		os.Stderr = f
	}

	// PID file
	if *pidFile != "" {
		if err := os.WriteFile(*pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0644); err != nil {
			log.Fatalf("cannot write pid file: %v", err)
		}
		defer os.Remove(*pidFile)
	}

	hostname, _ := os.Hostname()
	fmt.Printf("%s  Starting [kubjas] PID %d at host %q\n", time.Now().Format(time.DateTime), os.Getpid(), hostname)

	jobs, err := config.Load(*confFile, *confDir, nil)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	printJobList(jobs)

	sched, err := scheduler.New(jobs)
	if err != nil {
		log.Fatalf("creating scheduler: %v", err)
	}

	done := make(chan struct{})
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh,
		syscall.SIGHUP,
		syscall.SIGTERM,
		syscall.SIGINT,
		syscall.SIGUSR1,
		syscall.SIGUSR2,
	)

	go func() {
		for sig := range sigCh {
			switch sig {
			case syscall.SIGHUP:
				fmt.Printf("%s  Reading configuration files\n", time.Now().Format(time.DateTime))
				newJobs, err := config.Load(*confFile, *confDir, jobs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "reload error: %v\n", err)
					continue
				}
				jobs = newJobs
				printJobList(jobs)
				sched.Reload(jobs)
			case syscall.SIGUSR1:
				sched.PrintRunning()
			case syscall.SIGUSR2:
				sched.ToggleScheduling()
			case syscall.SIGTERM, syscall.SIGINT:
				fmt.Printf("%s  Shutdown\n", time.Now().Format(time.DateTime))
				close(done)
				return
			}
		}
	}()

	if err := sched.Run(done); err != nil {
		log.Fatalf("scheduler: %v", err)
	}
}

func printJobList(jobs []*config.Job) {
	fmt.Printf("%s  Reading configuration files\n", time.Now().Format(time.DateTime))
	for _, j := range jobs {
		fmt.Printf("%s  Job [%s] interval=%s\n", time.Now().Format(time.DateTime), j.Name, j.Interval)
	}
}
