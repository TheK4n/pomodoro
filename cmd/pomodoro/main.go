package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
	"os/exec"
)

const (
	socketPath = "/tmp/pomodoro.sock"
)

type Period int

const (
	Unknown Period = iota
	Work
	Rest
	Stopped
)

const (
	workDuration  = 25 * time.Minute
	restDuration  = 5 * time.Minute
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
	currentPeriod       Period
	currentRestOfTime   time.Duration
	initialPeriodDurations map[Period]time.Duration
	mu                  sync.RWMutex
}

func NewPomodoroDaemon() *PomodoroDaemon {
	return &PomodoroDaemon{
		currentPeriod: Work,
		currentRestOfTime: workDuration,
		initialPeriodDurations: map[Period]time.Duration{
			Work: workDuration,
			Rest: restDuration,
		},
	}
}

func (p *PomodoroDaemon) Start() error {
	if err := p.removeExistingSocket(); err != nil {
		return fmt.Errorf("failed to remove existing socket: %w", err)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("failed to create socket: %w", err)
	}
	defer listener.Close()

	p.currentPeriod = Stopped
	p.currentRestOfTime = 0

	go p.runTimer()

	fmt.Printf("Daemon started, socket: %s\n", socketPath)

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}

		go p.handleConnection(conn)
	}
}

func (p *PomodoroDaemon) removeExistingSocket() error {
	_ = os.Remove(socketPath)
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
	var title, message string

	p.currentPeriod = p.getReversedPeriod(p.currentPeriod)
	p.currentRestOfTime = p.initialPeriodDurations[p.currentPeriod]

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
	_ = cmd.Run()
}

func (p *PomodoroDaemon) handleConnection(conn net.Conn) {
	defer conn.Close()

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}

	command := strings.TrimSpace(string(buf[:n]))
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
		return
	}

	conn.Write(jsonData)
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

func (p *PomodoroDaemon) resetTimer() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.currentPeriod = Work
	p.currentRestOfTime = workDuration
}

func (p *PomodoroDaemon) toggleTimer() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.currentPeriod == Stopped {
		p.currentPeriod = Work
		p.currentRestOfTime = workDuration
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
	default:
		return "Unknown"
	}
}

func sendCommandToDaemon(command string) (*Response, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("error connecting to daemon: %w", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(command)); err != nil {
		return nil, fmt.Errorf("error sending command: %w", err)
	}

	buf := make([]byte, 1024)
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

func getFormatted() {
	response, err := sendCommandToDaemon("get")
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

func toggleTimer() {
	response, err := sendCommandToDaemon("switch")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Timer toggled. Status: %s %s\n", response.Status.Period, response.Status.RestOfTimeStr)
}

func formatDuration(d time.Duration) string {
	seconds := int(d.Seconds())
	hours := seconds / 3600
	seconds %= 3600
	minutes := seconds / 60
	seconds %= 60

	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s daemon | get | toggle\n", os.Args[0])
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "daemon":
		daemon := NewPomodoroDaemon()
		if err := daemon.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "Error starting daemon: %v\n", err)
			os.Exit(1)
		}
	case "get":
		getFormatted()
	case "toggle":
		toggleTimer()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		os.Exit(1)
	}
}
