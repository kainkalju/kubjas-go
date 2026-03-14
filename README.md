# kubjas - daemon to execute scheduled commands

## NAME

kubjas - (cron like) daemon to execute scheduled commands

## SYNOPSIS

```
kubjas [--conf_file /etc/kubjas.conf] [--conf_dir /etc/kubjas.d] [--log_file /path/kubjas.log] [--pid_file /path/kubjas.pid]
```

## DESCRIPTION

Kubjas is a periodic job scheduler that operates with minimum 1 second intervals.

Kubjas is not another cron daemon. Kubjas does not start programs at a certain time but at specified intervals. Kubjas also includes a **period** filter compatible with Perl's `Time::Period` syntax. You can configure **interval** and **period** combinations that act like crontab.

Kubjas measures executed job running times and logs them when a job exits. Measurements have millisecond resolution.

Kubjas configuration is standard INI file format. You can have multiple configuration files at the same time. Main configuration is `/etc/kubjas.conf` and `/etc/kubjas.d/` directory is for additional configurations. Each job can have its own config file. You can force configuration reload with the **HUP** signal.

### Platform support

| Feature | Linux | macOS |
|---|---|---|
| Filesystem watch | inotify | FSEvents / kqueue |
| ionice | yes (`/usr/bin/ionice`) | skipped |
| nice | yes | yes |
| UID/GID switching | yes (when running as root) | yes (when running as root) |
| UDP notify | yes | yes |

## BUILDING

```
go build -o kubjas .
```

Requires Go 1.21 or later.

## CONFIGURATION

### example.conf

```ini
[*]
notify-failure = 127.0.0.1:send_failure_notify

[date-job]
cmdline = date +"%H:%M" > /var/tmp/date.txt
interval = 60
user = nobody
group = nogroup
notify-success = 192.168.1.27:catch-signals

[catch-signals]
cmdline = /usr/local/bin/catch-signals.sh
interval = success-message
signal = USR2

[readfile]
cmdline = /usr/local/bin/readfile.sh
interval = onchange
watch = /var/tmp/date.txt
output = /tmp/date.log
user = nobody
group = nogroup

[very-shy-job]
cmdline = /usr/local/bin/shy.sh
interval = 10-20
period = wday {1 3 5 7} min {0-29}, wday {2 4 6} min {30-59}
depends = catch-signals
conflicts = date-job
nice = 1
ionice = 1

[send_failure_notify]
cmdline = send_failure_notify.sh %host% %job% %notify%
interval = failure-message
output = none
```

### job-name

`[job-name]` is the INI file section. Job names must be unique.

Special section name `[*]` sets default params that will be used with all jobs defined in the same configuration file. Named job sections overwrite default params.

### cmdline

Parameter **cmdline** defines the executable program with parameters:

```
cmdline = /usr/local/bin/catch-signals.sh
cmdline = catch-signals.sh
```

These lines are equivalent if the **PATH** environment variable includes `/usr/local/bin`.

Secure way is usage of full path names.

In combination with **watch** and **notify** you can add template parameters that will be filled with info at execution time:

```
cmdline = send_alert.sh %host% %job% %notify%
```

- `%host%` — replaced with hostname where notify originates
- `%job%` — replaced with job-name which sends the notify
- `%notify%` — replaced with notify message: `start-message`, `success-message`, `failure-message`, or filename that watch discovered a write event on

### output

Default is **passthrough** — all job STDOUT and/or STDERR will be passed through to kubjas STDOUT or log file (if defined with command line options).

Value **none** disables all output (equivalent to `cmdline 2>&1 >/dev/null`):

```
output = none
```

If the value is a filename, kubjas opens the file in append mode and forwards job STDOUT and STDERR to it:

```
output = /var/log/job-name.log
```

### interval

Specifies time in seconds between job last start. It is the minimum delay between different runs. Actual delay may be longer if other conditions prevent running. Null (`0`) means that job is disabled.

Interval can also be defined as a randomized range. Example starts job every 20 to 30 seconds:

```
interval = 20-30
```

There are also four special (non-numeric) intervals activated only by outside events: **onchange**, **start-message**, **success-message**, **failure-message**:

```
interval = onchange
interval = failure-message
```

**onchange** works with the **watch** parameter (see `watch`).

`start-message`, `success-message`, `failure-message` will trigger job execution when a notify message arrives (see `notify-start`).

### period

Parameter determines if a given time falls within a given period. Kubjas executes the job only if the period is met.

Period is an optional param.

Theoretically you can emulate **crontab** with **interval** and **period** combination. Example job will run only once a day at 0:00 midnight:

```
interval = 60
period = hr {12am} min {0}
```

A sub-period is of the form:

```
scale {range [range ...]} [scale {range [range ...]}]
```

Multiple sub-periods separated by commas are OR-ed together. All constraints within a single sub-period are AND-ed.

Scale must be one of nine different scales (or their equivalent codes):

| Scale  | Code | Valid Range Values |
|--------|------|--------------------|
| year   | yr   | n where n is an integer 0<=n<=99 or n>=1970 |
| month  | mo   | 1-12 or jan, feb, mar, apr, may, jun, jul, aug, sep, oct, nov, dec |
| week   | wk   | 1-6 |
| yday   | yd   | 1-365 |
| mday   | md   | 1-31 |
| wday   | wd   | 1-7 or su, mo, tu, we, th, fr, sa |
| hour   | hr   | 0-23 or 12am 1am-11am 12noon 12pm 1pm-11pm |
| minute | min  | 0-59 |
| second | sec  | 0-59 |

**crontab comparison [1]**

```
*/5 * * * *  nobody  cmdline
```

```
interval = 300
user = nobody
```

**crontab comparison [2]**

```
0 0 * * * 7  cmdline
```

```
interval = 60
period = wd {su} hr {12am} min {0}
```

or

```
interval = 1
period = wd {7} hr {0} min {0} sec {0}
```

**crontab comparison [3]**

```
# run at 2:15pm on the first of every month
15 14 1 * *  cmdline
```

```
period = md {1} hr {14} min {15} sec {0}
```

**crontab comparison [4]**

```
# run at 10 pm on weekdays
0 22 * * 1-5  cmdline
```

```
period = wd {Mon-Fri} hr {22} min {0} sec {0}
```

### user

Run jobs as given user. Kubjas resolves user UID. Requires kubjas to run as root.

### group

Run jobs as given group. Kubjas resolves group GID. Requires kubjas to run as root.

### watch

Kubjas monitors filesystem events if you specify a list of files and directories to **watch**.

One job can have many watch parameters. Kubjas monitors write/create events (equivalent to `IN_CLOSE_WRITE` on Linux). Example:

```
watch = /tmp
```

Will trigger job start whenever the `/tmp` directory changes. Only one instance of a job runs at a time.

On Linux, inotify is used directly. On macOS, FSEvents/kqueue is used via the same interface.

### notify-start, notify-success, notify-failure

Kubjas will notify any other local or remote jobs when the current job starts and ends. Other job configuration specifies when it runs at **start-message** or **success-message**. Example — two jobs that run after each other:

```ini
[job-one]
notify-success = 127.0.0.1:job-two
interval = success-message

[job-two]
notify-success = 127.0.0.1:job-one
interval = success-message
```

When a job exits with return code other than 0, you can send a failure notify to a job that fixes it or notifies the administrator:

```ini
[failure-handler]
cmdline = /usr/local/bin/send_email_to_admin.sh
interval = failure-message
```

### conflicts

This job will only run if no specified jobs are running. Useful for CPU-intensive jobs that should not overlap:

```ini
[hard-work]
conflicts = cpu-work1
conflicts = hard-work2
```

You can have multiple **conflicts** params.

The `conflicts` param can be the special wildcard value `*` to rule out any jobs defined in the same configuration file:

```ini
[special-job]
conflicts = *
```

### depends

This job will only run if dependencies are met (i.e., specified jobs are already running):

```
depends = other-job
```

You can have multiple **depends** params.

The `depends` param can be the special wildcard value `*` to require all other jobs defined in the same configuration file to be running:

```ini
[ping]
depends = *
```

### nice, ionice

Decrease executed job CPU and I/O scheduler priority:

```
nice = 1
ionice = 1
```

Will do `renice +10` and `ionice -c 3` (ionice is Linux only).

### signal

Combined with **interval** special cases, you can send UNIX signals to running jobs when a notify or watch event occurs:

```ini
[catch-signals]
interval = onchange
watch = /tmp/date.txt
signal = USR2
```

## SIGNALS

Kubjas handles the following signals:

### HUP

Reloads configuration. Does not affect running jobs.

```
kill -HUP <PID>
```

### USR1

Prints active jobs to log.

```
kill -USR1 <PID>
```

Example output: `2026-03-14 09:32:44  running (date-job readfile)`

### USR2

Stops scheduling new jobs. Useful before server maintenance — signal USR2, then watch the log and wait for all jobs to complete before shutdown or restart without breaking any running jobs.

```
kill -USR2 <PID>
```

## FILES

```
/etc/kubjas.conf
/etc/kubjas.d/
```

## SEE ALSO

`inotify(7)`, `fsnotify` (https://github.com/fsnotify/fsnotify)

## AUTHOR

Kain Kalju

Co-Authored-By: Claude Sonnet 4.6

## LICENSE

MIT License — Copyright (c) 2026 Kain Kalju.
See [LICENSE](LICENSE) for full text.
