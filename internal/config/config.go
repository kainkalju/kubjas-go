package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/ini.v1"
)

// Job holds configuration for a single scheduled job.
type Job struct {
	Name    string
	Cmdline string
	User    string
	Group   string

	// Interval: numeric seconds, "N-M" range, "onchange",
	// "start-message", "success-message", "failure-message"
	Interval string

	// Period: Time::Period expression, e.g. "mo {1-12}"
	Period string

	// Conflicts: job names that must NOT be running
	Conflicts []string

	// Depends: job names that MUST be running
	Depends []string

	// Watch: filesystem paths to monitor
	Watch []string

	// Notify targets: "host:job" or "127.0.0.1:job"
	NotifyStart   []string
	NotifySuccess []string
	NotifyFailure []string

	// Signal to send to running job on notify/watch trigger
	Signal string

	// Output: "passthrough", "none", or file path
	Output string

	// Nice: decrease CPU priority (renice +10)
	Nice bool

	// IONice: decrease I/O priority (ionice -c 3, Linux only)
	IONice bool

	// Runtime state (not from config)
	ExecTime int64 // unix timestamp seconds
	ExecMs   int64 // microseconds
}

func defaultJob(name string) *Job {
	return &Job{
		Name:     name,
		User:     "root",
		Group:    "root",
		Interval: "0",
		Period:   "mo {1-12}",
		Output:   "passthrough",
	}
}

// splitMultiValue splits a value that may contain newlines (ini multi-value)
// or commas into individual entries.
func splitMultiValue(s string) []string {
	var result []string
	for _, part := range strings.Split(s, "\n") {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func applyKey(job *Job, key, val string) {
	val = strings.TrimSpace(val)
	if val == "" {
		return
	}
	switch strings.ToLower(key) {
	case "cmdline":
		job.Cmdline = val
	case "user":
		job.User = val
	case "group":
		job.Group = val
	case "interval":
		job.Interval = val
	case "period":
		job.Period = val
	case "conflicts":
		job.Conflicts = append(job.Conflicts, splitMultiValue(val)...)
	case "depends":
		job.Depends = append(job.Depends, splitMultiValue(val)...)
	case "watch":
		job.Watch = append(job.Watch, splitMultiValue(val)...)
	case "notify-start":
		job.NotifyStart = append(job.NotifyStart, splitMultiValue(val)...)
	case "notify-success":
		job.NotifySuccess = append(job.NotifySuccess, splitMultiValue(val)...)
	case "notify-failure":
		job.NotifyFailure = append(job.NotifyFailure, splitMultiValue(val)...)
	case "signal":
		job.Signal = val
	case "output":
		job.Output = val
	case "nice":
		job.Nice = val == "1" || strings.ToLower(val) == "true"
	case "ionice":
		job.IONice = val == "1" || strings.ToLower(val) == "true"
	}
}

func defaultKeys() []string {
	return []string{
		"cmdline", "user", "group", "interval", "period",
		"conflicts", "depends", "watch",
		"notify-start", "notify-success", "notify-failure",
		"signal", "output", "nice", "ionice",
	}
}

// Load reads all configuration files and returns a list of jobs.
// previous is used to preserve exec_time/exec_ms across reloads.
func Load(mainConf, confDir string, previous []*Job) ([]*Job, error) {
	// Preserve exec times from previous load
	prevTime := map[string]int64{}
	prevMs := map[string]int64{}
	for _, j := range previous {
		prevTime[j.Name] = j.ExecTime
		prevMs[j.Name] = j.ExecMs
	}

	cfgFiles := []string{}
	if _, err := os.Stat(mainConf); err == nil {
		cfgFiles = append(cfgFiles, mainConf)
	}

	entries, err := os.ReadDir(confDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading config dir %s: %w", confDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, "dpkg-old") || strings.HasSuffix(name, "~") {
			continue
		}
		full := filepath.Join(confDir, name)
		if info, err := os.Stat(full); err != nil || info.Mode()&0444 == 0 {
			continue
		}
		cfgFiles = append(cfgFiles, full)
	}

	seen := map[string]bool{}
	var jobs []*Job

	for _, cfgFile := range cfgFiles {
		f, err := ini.LoadSources(ini.LoadOptions{
			IgnoreInlineComment:         true,
			AllowShadows:                true, // multi-value keys
			AllowNonUniqueSections:      false,
			SpaceBeforeInlineComment:    true,
			UnescapeValueDoubleQuotes:   false,
			UnescapeValueCommentSymbols: false,
		}, cfgFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: cannot parse %s: %v\n", cfgFile, err)
			continue
		}
		f.NameMapper = strings.ToLower

		// Build defaults from [*] section
		defaults := map[string]string{}
		if sec, err := f.GetSection("*"); err == nil {
			for _, key := range defaultKeys() {
				if k, err := sec.GetKey(key); err == nil {
					defaults[key] = strings.Join(k.ValueWithShadows(), "\n")
				}
			}
		}

		// Collect all section names for wildcard expansion
		var allNames []string
		for _, sec := range f.Sections() {
			if sec.Name() == ini.DefaultSection || sec.Name() == "*" {
				continue
			}
			allNames = append(allNames, sec.Name())
		}

		for _, sec := range f.Sections() {
			secName := sec.Name()
			if secName == ini.DefaultSection || secName == "*" {
				continue
			}
			if seen[secName] {
				fmt.Fprintf(os.Stderr, "warn: duplicate job [%s] in %s\n", secName, cfgFile)
				continue
			}
			seen[secName] = true

			job := defaultJob(secName)

			// Apply defaults first
			for k, v := range defaults {
				applyKey(job, k, v)
			}

			// Apply section values (override defaults)
			for _, key := range defaultKeys() {
				if k, err := sec.GetKey(key); err == nil {
					// Reset slice fields before applying so section overrides defaults
					switch strings.ToLower(key) {
					case "conflicts", "depends", "watch",
						"notify-start", "notify-success", "notify-failure":
						// Don't reset — accumulate (Perl behavior: multi-value appends)
					default:
						// scalar: just apply
					}
					applyKey(job, key, strings.Join(k.ValueWithShadows(), "\n"))
				}
			}

			// Expand wildcard conflicts/depends
			if len(job.Conflicts) == 1 && job.Conflicts[0] == "*" {
				job.Conflicts = allNames
			}
			if len(job.Depends) == 1 && job.Depends[0] == "*" {
				job.Depends = allNames
			}

			// Restore exec times
			job.ExecTime = prevTime[secName]
			job.ExecMs = prevMs[secName]

			jobs = append(jobs, job)
		}
	}

	return jobs, nil
}
