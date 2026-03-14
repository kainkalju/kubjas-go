package scheduler

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"kubjas/internal/config"
	"kubjas/internal/job"
	"kubjas/internal/notify"
	"kubjas/internal/period"
	"kubjas/internal/watch"
)

// Scheduler is the main event loop.
type Scheduler struct {
	mu         sync.Mutex
	jobs       []*config.Job
	running    map[string]int // job name -> PID
	noNewJobs  bool
	startTime  time.Time
	watcher    *watch.Watcher
	notifyCh   chan notify.Message
	watchedSet map[string]bool
}

// New creates a Scheduler with the initial job list.
func New(jobs []*config.Job) (*Scheduler, error) {
	w, err := watch.New()
	if err != nil {
		return nil, err
	}
	s := &Scheduler{
		jobs:       jobs,
		running:    make(map[string]int),
		startTime:  time.Now(),
		watcher:    w,
		notifyCh:   make(chan notify.Message, 64),
		watchedSet: make(map[string]bool),
	}
	s.applyWatches()
	return s, nil
}

// Reload replaces the job list (called on SIGHUP).
func (s *Scheduler) Reload(jobs []*config.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = jobs
	s.applyWatches()
}

// PrintRunning logs currently running jobs.
func (s *Scheduler) PrintRunning() {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.running))
	for n := range s.running {
		names = append(names, n)
	}
	fmt.Printf("%s  running (%s)\n", time.Now().Format(time.DateTime), strings.Join(names, " "))
}

// ToggleScheduling flips no-new-jobs mode.
func (s *Scheduler) ToggleScheduling() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noNewJobs = !s.noNewJobs
	if s.noNewJobs {
		fmt.Printf("%s  Switching job scheduling OFF\n", time.Now().Format(time.DateTime))
	} else {
		fmt.Printf("%s  Switching job scheduling ON\n", time.Now().Format(time.DateTime))
	}
}

// Run starts the event loop. It blocks until done is closed.
func (s *Scheduler) Run(done <-chan struct{}) error {
	if err := notify.Listen(s.notifyCh, done); err != nil {
		fmt.Fprintf(os.Stderr, "warn: UDP listener unavailable: %v\n", err)
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	watchEvents := s.watcher.Events()

	for {
		select {
		case <-done:
			return nil
		case <-ticker.C:
			s.triggerTime()
		case ev := <-watchEvents:
			s.triggerWatch(ev.Path)
		case msg := <-s.notifyCh:
			s.triggerNotify(msg)
		}
	}
}

func (s *Scheduler) applyWatches() {
	for _, j := range s.jobs {
		for _, path := range j.Watch {
			if !s.watchedSet[path] {
				if err := s.watcher.Add(path); err != nil {
					fmt.Fprintf(os.Stderr, "warn: %v\n", err)
				} else {
					s.watchedSet[path] = true
				}
			}
		}
	}
}

var lastTimeTick time.Time

func (s *Scheduler) triggerTime() {
	now := time.Now()
	if now.Sub(lastTimeTick) < time.Second {
		return
	}
	lastTimeTick = now
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startJobs("time", "", "", "", "")
}

func (s *Scheduler) triggerWatch(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	myhost, _ := os.Hostname()
	s.startJobs("watch", path, "", "kubjas", myhost)
}

func (s *Scheduler) triggerNotify(msg notify.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// toJob, notify, fromJob, fromHost
	s.startJobs("notify", msg.ToJob, msg.Notify, msg.FromJob, msg.FromHost)
}

// startJobs must be called with s.mu held.
// trigger: "time" | "watch" | "notify"
// For watch: arg1=path, fromJob="kubjas", fromHost=hostname
// For notify: arg1=toJobName, notifyType=msg, fromJob, fromHost
func (s *Scheduler) startJobs(trigger, arg1, notifyType, fromJob, fromHost string) {
	if s.noNewJobs {
		return
	}
	now := time.Now()
	sorted := false

	for _, j := range s.jobs {
		name := j.Name

		// Notify trigger: only target job
		if trigger == "notify" && arg1 != name {
			continue
		}

		// Period check
		ok, err := period.InPeriod(now, j.Period)
		if err != nil {
			fmt.Fprintf(os.Stderr, "period error [%s]: %v\n", name, err)
			continue
		}
		if !ok {
			continue
		}

		if s.inConflicts(j) || s.noDepends(j) {
			continue
		}

		interval := strings.TrimSpace(j.Interval)
		if interval == "" || interval == "0" {
			continue
		}

		// Randomized range interval
		intervalSecs := -1
		if lo, hi, ok := parseRange(interval); ok {
			diff := hi - lo
			if diff < 0 {
				diff = -diff
			}
			intervalSecs = rand.Intn(diff+1) + lo
		} else if n, err := strconv.Atoi(interval); err == nil {
			intervalSecs = n
		}

		switch trigger {
		case "watch":
			if strings.ToLower(interval) != "onchange" {
				continue
			}
			if !s.inWatch(j, arg1) {
				continue
			}
		case "notify":
			if strings.ToLower(interval) != notifyType {
				continue
			}
		case "time":
			if intervalSecs < 0 {
				continue
			}
			elapsed := now.Unix() - j.ExecTime
			sinceStart := now.Unix() - s.startTime.Unix()
			if elapsed < int64(intervalSecs) || sinceStart < int64(intervalSecs) {
				continue
			}
		}

		// Signal running job if requested
		if j.Signal != "" && (trigger == "notify" || trigger == "watch") {
			if pid, ok := s.running[name]; ok {
				sendSignal(pid, j.Signal)
			}
		}

		if _, alreadyRunning := s.running[name]; alreadyRunning {
			continue
		}

		// Resolve cmdline template for notify/watch
		cmdline := j.Cmdline
		if strings.Contains(cmdline, "%") {
			cmdline = job.ApplyCmdlineEnv(cmdline, fromHost, fromJob, notifyType)
		}
		jCopy := *j
		jCopy.Cmdline = cmdline

		cmd, err := job.Start(&jCopy)
		if err != nil {
			fmt.Printf("%s  FAILED EXEC %s: %v\n", now.Format(time.DateTime), name, err)
			j.ExecTime = now.Unix()
			continue
		}

		pid := cmd.Process.Pid
		fmt.Printf("%s  EXEC [%s] PID %d\n", now.Format(time.DateTime), name, pid)
		s.running[name] = pid
		j.ExecTime = now.Unix()
		j.ExecMs = now.UnixMicro() % 1_000_000
		sorted = true

		// Send notify-start
		for _, target := range j.NotifyStart {
			notify.Send(target, name, "start-message")
		}

		// Wait for exit in goroutine
		go s.waitJob(cmd, j)
	}

	if sorted {
		sortJobs(s.jobs)
	}
}

func (s *Scheduler) waitJob(cmd *exec.Cmd, j *config.Job) {
	err := cmd.Wait()
	name := j.Name
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(interface{ ExitCode() int }); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	elapsed := elapsedSince(j.ExecTime, j.ExecMs)

	s.mu.Lock()
	pid := s.running[name]
	delete(s.running, name)
	s.mu.Unlock()

	if exitCode != 0 {
		fmt.Printf("%s  PID %d exited [%s] running time %s.\n", time.Now().Format(time.DateTime), pid, name, elapsed)
		fmt.Printf("%s  FAILURE: PID %d exited with status %d\n", time.Now().Format(time.DateTime), pid, exitCode)
		for _, target := range j.NotifyFailure {
			notify.Send(target, name, "failure-message")
		}
	} else {
		fmt.Printf("%s  PID %d exited [%s] running time %s.\n", time.Now().Format(time.DateTime), pid, name, elapsed)
		for _, target := range j.NotifySuccess {
			notify.Send(target, name, "success-message")
		}
	}
}

func (s *Scheduler) inConflicts(j *config.Job) bool {
	for _, c := range j.Conflicts {
		if _, ok := s.running[c]; ok {
			return true
		}
	}
	return false
}

func (s *Scheduler) noDepends(j *config.Job) bool {
	for _, d := range j.Depends {
		if _, ok := s.running[d]; !ok {
			return true
		}
	}
	return false
}

func (s *Scheduler) inWatch(j *config.Job, path string) bool {
	for _, w := range j.Watch {
		if strings.HasPrefix(path, w) {
			return true
		}
	}
	return false
}

func parseRange(s string) (lo, hi int, ok bool) {
	idx := strings.Index(s, "-")
	if idx <= 0 {
		return 0, 0, false
	}
	lo, err1 := strconv.Atoi(s[:idx])
	hi, err2 := strconv.Atoi(s[idx+1:])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return lo, hi, true
}

func sortJobs(jobs []*config.Job) {
	// Insertion sort by exec_time (small N, stable)
	for i := 1; i < len(jobs); i++ {
		for j := i; j > 0 && jobs[j].ExecTime < jobs[j-1].ExecTime; j-- {
			jobs[j], jobs[j-1] = jobs[j-1], jobs[j]
		}
	}
}

func elapsedSince(execTime, execMs int64) string {
	now := time.Now()
	startSec := time.Unix(execTime, execMs*1000)
	d := now.Sub(startSec)
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("%.3fs", d.Seconds())
	}
	total := int(d.Seconds())
	days := total / 86400
	total %= 86400
	hours := total / 3600
	total %= 3600
	mins := total / 60
	secs := total % 60

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 {
		parts = append(parts, fmt.Sprintf("%dm", mins))
	}
	if secs > 0 {
		parts = append(parts, fmt.Sprintf("%ds", secs))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%.3fs", d.Seconds())
	}
	return strings.Join(parts, " ")
}

func sendSignal(pid int, sig string) {
	// Try numeric first
	if n, err := strconv.Atoi(sig); err == nil {
		if proc, err := os.FindProcess(pid); err == nil {
			proc.Signal(signalFromInt(n))
		}
		return
	}
	// Named signal
	if proc, err := os.FindProcess(pid); err == nil {
		proc.Signal(signalByName(sig))
	}
}
