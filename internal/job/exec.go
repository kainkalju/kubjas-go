package job

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"kubjas/internal/config"
)

// RunningJob tracks an executing child process.
type RunningJob struct {
	Cmd     *exec.Cmd
	Job     *config.Job
	Started time.Time
}

// IsExecutable checks whether the command (first word of cmdline) is executable.
func IsExecutable(cmdline string) bool {
	if cmdline == "" {
		return false
	}
	cmd := strings.Fields(cmdline)[0]
	if _, err := exec.LookPath(cmd); err == nil {
		return true
	}
	return false
}

// ApplyCmdlineEnv substitutes %host%, %job%, %notify% placeholders.
func ApplyCmdlineEnv(cmdline, host, fromJob, notify string) string {
	cmdline = strings.ReplaceAll(cmdline, "%host%", host)
	cmdline = strings.ReplaceAll(cmdline, "%job%", fromJob)
	cmdline = strings.ReplaceAll(cmdline, "%notify%", notify)
	return cmdline
}

// Start launches the job as a child process and returns the exec.Cmd.
// The caller is responsible for waiting on the command.
func Start(j *config.Job) (*exec.Cmd, error) {
	parts := strings.Fields(j.Cmdline)
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty cmdline for job %s", j.Name)
	}

	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Stdin = nil

	// Output routing
	switch {
	case j.Output == "none" || j.Output == "":
		cmd.Stdout = nil
		cmd.Stderr = nil
	case j.Output == "passthrough":
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	default:
		f, err := os.OpenFile(j.Output, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("open output %s: %w", j.Output, err)
		}
		cmd.Stdout = f
		cmd.Stderr = f
	}

	// Build SysProcAttr with UID/GID and new session
	attr, err := buildSysProcAttr(j)
	if err != nil {
		return nil, err
	}
	cmd.SysProcAttr = attr

	// Apply nice before starting (post-exec via renice is also used)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", j.Cmdline, err)
	}

	// Apply nice/ionice after start
	if j.Nice {
		if path, err := exec.LookPath("renice"); err == nil {
			renice := exec.Command(path, "+10", strconv.Itoa(cmd.Process.Pid))
			renice.Run()
		} else {
			fmt.Println("WARN: cannot find renice")
		}
	}
	if j.IONice && runtime.GOOS == "linux" {
		if path, err := exec.LookPath("ionice"); err == nil {
			ionice := exec.Command(path, "-c", "3", "-p", strconv.Itoa(cmd.Process.Pid))
			ionice.Run()
		} else {
			fmt.Println("WARN: cannot find ionice")
		}
	}

	return cmd, nil
}

func buildSysProcAttr(j *config.Job) (*syscall.SysProcAttr, error) {
	attr := &syscall.SysProcAttr{
		Setsid: true,
	}

	// Only attempt UID/GID switching when running as root.
	if os.Getuid() != 0 {
		return attr, nil
	}

	userName := j.User
	if userName == "" {
		userName = "root"
	}
	groupName := j.Group
	if groupName == "" {
		groupName = userName
	}

	u, err := user.Lookup(userName)
	if err != nil {
		return nil, fmt.Errorf("user %q not found: %w", userName, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid uid for %s: %w", userName, err)
	}

	var gid uint64
	g, err := user.LookupGroup(groupName)
	if err != nil {
		gid, _ = strconv.ParseUint(u.Gid, 10, 32)
	} else {
		gid, _ = strconv.ParseUint(g.Gid, 10, 32)
	}

	attr.Credential = &syscall.Credential{
		Uid: uint32(uid),
		Gid: uint32(gid),
	}
	return attr, nil
}
