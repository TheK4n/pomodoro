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

// Period represents the current Pomodoro period type.
type Period int

const (
	Unknown Period = iota
	Work
	Rest
	Stopped // Добавлен новый тип периода для остановленного состояния
)

const (
	workDuration  = 25 * time.Minute
	restDuration  = 5 * time.Minute
)

// Status holds the current state of the Pomodoro timer.
type Status struct {
	Period        string        `json:"period"`
	RestOfTime    time.Duration `json:"rest_of_time"`
	RestOfTimeStr string        `json:"rest_of_time_str"`
}

// Response is the JSON response sent by the daemon.
type Response struct {
	Status *Status `json:"status,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// PomodoroDaemon manages the timer state and Unix socket server.
type PomodoroDaemon struct {
	currentPeriod       Period
	currentRestOfTime   time.Duration
	initialPeriodDurations map[Period]time.Duration
	mu                  sync.RWMutex
}

// NewPomodoroDaemon initializes a new daemon instance.
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

// Start begins the timer logic and the Unix socket server.
func (p *PomodoroDaemon) Start() error {
	if err := p.removeExistingSocket(); err != nil {
		return fmt.Errorf("failed to remove existing socket: %w", err)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("failed to create socket: %w", err)
	}
	defer listener.Close()

	// Start the internal timer loop
	go p.runTimer()

	fmt.Printf("Daemon started, socket: %s\n", socketPath)

	// Accept connections
	for {
		conn, err := listener.Accept()
		if err != nil {
			// Log error and continue accepting other connections
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

	args := []string{"-t", "5000", "-a", "Pomodoro Timer"} // 5000ms = 5s, -a для указания приложения

	if p.currentPeriod == Work {
		title = "Pomodoro: Work Time!"
		message = "Time to focus! Start your work session."
	} else { // p.currentPeriod == Rest
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
		return // Connection likely closed by client
	}

	command := strings.TrimSpace(string(buf[:n]))
	var response Response

	switch command {
	case "get":
		status := p.getStatus()
		response.Status = &status
	case "switch":
		p.toggleTimer()
		status := p.getStatus() // Get status after toggle
		response.Status = &status
	default:
		response.Error = "Unknown command"
	}

	jsonData, err := json.Marshal(response)
	if err != nil {
		// Log error, but no point sending error back after failure to marshal
		return
	}

	conn.Write(jsonData)
}

// getStatus safely retrieves the current status.
func (p *PomodoroDaemon) getStatus() Status {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Возвращаем статус в зависимости от состояния
	if p.currentPeriod == Stopped {
		return Status{
			Period:        "Stopped",
			RestOfTime:    0, // Время остановлено на 0
			RestOfTimeStr: "00:00",
		}
	}

	return Status{
		Period:        p.periodToString(p.currentPeriod),
		RestOfTime:    p.currentRestOfTime,
		RestOfTimeStr: formatDuration(p.currentRestOfTime),
	}
}

// resetTimer safely resets the timer to the initial Work state.
func (p *PomodoroDaemon) resetTimer() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.currentPeriod = Work
	p.currentRestOfTime = workDuration
}

// toggleTimer safely toggles the timer between running and stopped states.
func (p *PomodoroDaemon) toggleTimer() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.currentPeriod == Stopped {
		// Если таймер остановлен, переключаемся обратно в рабочий режим
		p.currentPeriod = Work
		p.currentRestOfTime = workDuration
	} else {
		// Если таймер запущен, останавливаем его
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

// sendCommandToDaemon connects to the daemon socket and sends a command.
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

func getTime() {
	response, err := sendCommandToDaemon("get")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(response.Status.RestOfTimeStr)
}

func getPeriod() {
	response, err := sendCommandToDaemon("get")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(response.Status.Period)
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

// toggleTimer sends a switch command to the daemon.
func toggleTimer() {
	response, err := sendCommandToDaemon("switch")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	// Print the status after toggle
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
		fmt.Fprintf(os.Stderr, "Usage: %s -daemon | -time | -period | -formatted | -switch\n", os.Args[0])
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
	case "time":
		getTime()
	case "period":
		getPeriod()
	case "formatted":
		getFormatted()
	case "switch":
		toggleTimer()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		os.Exit(1)
	}
}
