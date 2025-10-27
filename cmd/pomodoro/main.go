// Pomodoro enter point.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"time"

	flags "github.com/jessevdk/go-flags"
)

type options struct {
	SocketPath  string `long:"socket-path" default:"" env:"SOCKET_PATH" description:"Path to socket"`
	WorkMinutes int    `long:"work" short:"w" default:"25" description:"Time period for work in minutes"`
	RestMinutes int    `long:"rest" short:"r" default:"5" description:"Time period for rest in minutes"`
}

func (opts *options) SetDefaultSocketPathIfNotProvided() {
	if opts.SocketPath != "" {
		return
	}

	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		opts.SocketPath = path.Join(runtimeDir, "pomodoro.sock")
		return
	}

	opts.SocketPath = "/tmp/pomodoro.sock"
}

type Period int

const (
	Unknown Period = iota
	Work
	Rest
	Stopped
)

const (
	OneKebibyte = 1024
)

type Status struct {
	Period        string        `json:"period"`
	RestOfTime    time.Duration `json:"rest_of_time"`
	RestOfTimeStr string        `json:"rest_of_time_str"`
}

type Response struct {
	Status *Status `json:"status,omitempty"`
	Error  string  `json:"error,omitempty"`
}

type PomodoroDaemon struct {
	mu                     sync.RWMutex
	socketPath             string
	currentPeriod          Period
	currentRestOfTime      time.Duration
	initialPeriodDurations map[Period]time.Duration
}

func NewPomodoroDaemon(socketPath string, workDuration, restDuration time.Duration) *PomodoroDaemon {
	return &PomodoroDaemon{
		socketPath:        socketPath,
		currentPeriod:     Work,
		currentRestOfTime: workDuration,
		initialPeriodDurations: map[Period]time.Duration{
			Work: workDuration,
			Rest: restDuration,
		},
	}
}

func (p *PomodoroDaemon) Start() error {
	if err := p.removeExistingSocket(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to remove existing socket: %e", err)
	}

	listener, err := net.Listen("unix", p.socketPath)
	if err != nil {
		return fmt.Errorf("failed to create socket: %w", err)
	}

	defer func() {
		_ = listener.Close()
	}()

	p.currentPeriod = Stopped
	p.currentRestOfTime = 0

	go p.runTimer()

	fmt.Printf("Daemon started, socket: %s\n", p.socketPath)

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}

		go p.handleConnection(conn)
	}
}

func (p *PomodoroDaemon) removeExistingSocket() error {
	if err := os.Remove(p.socketPath); err != nil {
		return fmt.Errorf("fail to remove socket: %w", err)
	}

	return nil
}

func (p *PomodoroDaemon) runTimer() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		p.mu.Lock()

		if p.currentPeriod != Stopped && p.currentRestOfTime <= 1*time.Second {
			p.switchTimer()
		} else if p.currentPeriod != Stopped {
			p.currentRestOfTime -= 1 * time.Second
		}

		p.mu.Unlock()
	}
}

func (p *PomodoroDaemon) switchTimer() {
	p.currentPeriod = p.getReversedPeriod(p.currentPeriod)
	p.currentRestOfTime = p.initialPeriodDurations[p.currentPeriod]

	_ = p.notifySend()
}

func (p *PomodoroDaemon) notifySend() error {
	var title, message string

	args := []string{"-t", "5000", "-a", "Pomodoro Timer"}

	if p.currentPeriod == Work {
		title = "Pomodoro: Work Time!"
		message = "Time to focus! Start your work session."
	} else {
		title = "Pomodoro: Break Time!"
		message = "Take a break and relax."
	}

	args = append(args, title, message)

	cmd := exec.Command("notify-send", args...)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("fail to notify: %w", err)
	}

	return nil
}

func (p *PomodoroDaemon) handleConnection(conn net.Conn) {
	defer func() {
		_ = conn.Close()
	}()

	buf := make([]byte, OneKebibyte)

	n, err := conn.Read(buf)
	if err != nil {
		return
	}

	command := strings.TrimSpace(string(buf[:n]))

	jsonData, err := p.dispatchCommand(command)
	if err != nil {
		return
	}

	_, err = conn.Write(jsonData)
	if err != nil {
		return
	}
}

func (p *PomodoroDaemon) dispatchCommand(command string) ([]byte, error) {
	var response Response

	switch command {
	case "get":
		status := p.getStatus()
		response.Status = &status
	case "switch":
		p.toggleTimer()
		status := p.getStatus()
		response.Status = &status
	default:
		response.Error = "Unknown command"
	}

	jsonData, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("fail to marshal response: %w", err)
	}

	return jsonData, nil
}

func (p *PomodoroDaemon) getStatus() Status {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.currentPeriod == Stopped {
		return Status{
			Period:        "Stopped",
			RestOfTime:    0,
			RestOfTimeStr: "00:00",
		}
	}

	return Status{
		Period:        p.periodToString(p.currentPeriod),
		RestOfTime:    p.currentRestOfTime,
		RestOfTimeStr: formatDuration(p.currentRestOfTime),
	}
}

func (p *PomodoroDaemon) toggleTimer() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.currentPeriod == Stopped {
		p.currentPeriod = Work
		p.currentRestOfTime = p.initialPeriodDurations[Work]
	} else {
		p.currentPeriod = Stopped
		p.currentRestOfTime = 0
	}
}

func (p *PomodoroDaemon) getReversedPeriod(current Period) Period {
	if current == Work {
		return Rest
	}

	return Work
}

func (p *PomodoroDaemon) periodToString(period Period) string {
	switch period {
	case Work:
		return "Work"
	case Rest:
		return "Rest"
	case Stopped:
		return "Stopped"
	case Unknown:
		return "Unknown"
	default:
		return "Unknown"
	}
}

func sendCommandToDaemon(command string, socketPath string) (*Response, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("error connecting to daemon: %w", err)
	}

	defer func() {
		_ = conn.Close()
	}()

	if _, err := conn.Write([]byte(command)); err != nil {
		return nil, fmt.Errorf("error sending command: %w", err)
	}

	buf := make([]byte, OneKebibyte)

	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("error reading response: %w", err)
	}

	var response Response
	if err := json.Unmarshal(buf[:n], &response); err != nil {
		return nil, fmt.Errorf("error parsing JSON response: %w", err)
	}

	if response.Error != "" {
		return nil, fmt.Errorf("daemon error: %s", response.Error)
	}

	return &response, nil
}

func getFormatted(socketPath string) {
	response, err := sendCommandToDaemon("get", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	var emoji string

	switch response.Status.Period {
	case "Work":
		emoji = "🍅"
	case "Rest":
		emoji = "😋"
	case "Stopped":
		emoji = "⏸️"
	default:
		emoji = "❓"
	}

	fmt.Printf("%s %s\n", emoji, response.Status.RestOfTimeStr)
}

func toggleTimer(socketPath string) {
	response, err := sendCommandToDaemon("switch", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Timer toggled. Status: %s %s\n", response.Status.Period, response.Status.RestOfTimeStr)
}

func formatDuration(d time.Duration) string {
	secondsInHour := 3600
	secondsInMinute := 60

	seconds := int(d.Seconds())
	hours := seconds / secondsInHour
	seconds %= secondsInHour
	minutes := seconds / secondsInMinute
	seconds %= secondsInMinute

	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	}

	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

func main() {
	var opts options

	args, err := flags.NewParser(&opts, flags.Default).ParseArgs(os.Args)
	if err != nil {
		fmt.Printf("parse params error: %s\n", err)
		os.Exit(1)
	}

	opts.SetDefaultSocketPathIfNotProvided()

	commandProvidedArgsLen := 2
	if len(args) < commandProvidedArgsLen {
		fmt.Fprintf(os.Stderr, "Usage: %s daemon | get | toggle\n", args[0])
		os.Exit(1)
	}

	command := args[1]

	switch command {
	case "daemon":
		daemon := NewPomodoroDaemon(
			opts.SocketPath,
			time.Duration(opts.WorkMinutes)*time.Minute,
			time.Duration(opts.RestMinutes)*time.Minute,
		)
		if err := daemon.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "Error starting daemon: %v\n", err)
			os.Exit(1)
		}
	case "get":
		getFormatted(opts.SocketPath)
	case "toggle":
		toggleTimer(opts.SocketPath)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		os.Exit(1)
	}
}
