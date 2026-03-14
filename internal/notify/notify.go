package notify

import (
	"fmt"
	"hash/crc32"
	"net"
	"os"
	"strings"
	"time"
)

const port = 2380

// Message is a parsed inbound notify message.
type Message struct {
	ToJob    string
	Notify   string // start-message | success-message | failure-message | ping
	FromJob  string
	FromHost string
}

var validNotifyTypes = map[string]bool{
	"start-message":   true,
	"success-message": true,
	"failure-message": true,
	"ping":            true,
}

// Listener receives UDP notify messages and sends parsed messages to ch.
// It blocks until the context is cancelled via the done channel.
func Listen(ch chan<- Message, done <-chan struct{}) error {
	addr := &net.UDPAddr{Port: port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("udp listen :%d: %w", port, err)
	}
	go func() {
		<-done
		conn.Close()
	}()
	go func() {
		filter := newDupFilter(20)
		buf := make([]byte, 4096)
		for {
			n, remote, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			packet := strings.TrimSpace(string(buf[:n]))
			if filter.isDup(packet) {
				continue
			}
			fmt.Printf("%s  notify from %s {%s}\n", time.Now().Format(time.DateTime), remote, packet)
			msg, ok := parsePacket(packet)
			if !ok {
				continue
			}
			// Send OK reply
			conn.WriteToUDP([]byte("OK\n"), remote)
			ch <- msg
		}
	}()
	return nil
}

func parsePacket(packet string) (Message, bool) {
	// Format: "remote_host to_job from_job notify timestamp"
	parts := strings.Fields(packet)
	if len(parts) < 4 {
		return Message{}, false
	}
	// parts[0]=remote_host, parts[1]=to_job, parts[2]=from_job, parts[3]=notify
	notify := parts[3]
	if !validNotifyTypes[notify] {
		return Message{}, false
	}
	return Message{
		FromHost: parts[0],
		ToJob:    parts[1],
		FromJob:  parts[2],
		Notify:   notify,
	}, true
}

// Send sends a notify message to remote (format "host:job") on behalf of myJob.
// Runs in a goroutine and retries up to 3 times.
func Send(remote, myJob, notify string) {
	go func() {
		parts := strings.SplitN(remote, ":", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "notify: invalid remote %q\n", remote)
			return
		}
		host, remoteJob := parts[0], parts[1]
		myhost, _ := os.Hostname()
		timest := time.Now().Unix()
		message := fmt.Sprintf("%s %s %s %s %d", myhost, remoteJob, myJob, notify, timest)

		addr := fmt.Sprintf("%s:%d", host, port)
		conn, err := net.DialTimeout("udp", addr, 3*time.Second)
		if err != nil {
			fmt.Fprintf(os.Stderr, "notify: dial %s: %v\n", addr, err)
			return
		}
		defer conn.Close()

		buf := make([]byte, 1024)
		for i := 0; i < 3; i++ {
			conn.SetDeadline(time.Now().Add(time.Second))
			conn.Write([]byte(message))
			n, err := conn.Read(buf)
			if err == nil && n >= 2 && string(buf[:2]) == "OK" {
				return
			}
		}
		fmt.Fprintf(os.Stderr, "WARN: %s did not respond to notify!\n", host)
	}()
}

// dupFilter keeps a LIFO fingerprint cache to drop duplicate messages.
type dupFilter struct {
	max   int
	seen  map[uint32]string
	order []uint32
}

func newDupFilter(max int) *dupFilter {
	return &dupFilter{max: max, seen: make(map[uint32]string)}
}

func (f *dupFilter) isDup(msg string) bool {
	fp := crc32.ChecksumIEEE([]byte(msg))
	if existing, ok := f.seen[fp]; ok && existing == msg {
		return true
	}
	f.seen[fp] = msg
	f.order = append(f.order, fp)
	if len(f.order) > f.max {
		old := f.order[0]
		f.order = f.order[1:]
		delete(f.seen, old)
	}
	return false
}
