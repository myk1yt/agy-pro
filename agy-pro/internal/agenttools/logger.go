package agenttools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"agypro/internal/providers"
)

// LogEntry represents an executed tool call recorded in the JSONL log.
type LogEntry struct {
	Timestamp  string `json:"timestamp"`
	SeatID     int    `json:"seat_id"`
	Model      string `json:"model"`
	Provider   string `json:"provider"`
	Tool       string `json:"tool"`
	Arguments  string `json:"arguments"`
	Output     string `json:"output"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// Logger handles thread-safe writing of tool execution traces.
type Logger struct {
	path    string
	mu      sync.Mutex
	redact  func(string) string
	enabled bool
}

// NewLogger creates a new JSONL tool logger at the specified file path.
func NewLogger(logPath string, redactor func(string) string) (*Logger, error) {
	if logPath == "" {
		return &Logger{enabled: false}, nil
	}
	dir := filepath.Dir(logPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}
	if redactor == nil {
		// Fail closed: the audit log is a durable sink for tool output and
		// upstream error text, so a missing redactor still masks the generic
		// credential shapes.
		redactor = providers.NewRedactor()
	}
	return &Logger{
		path:    logPath,
		redact:  redactor,
		enabled: true,
	}, nil
}

// Log records a tool execution event to the JSONL log file.
func (l *Logger) Log(entry LogEntry) error {
	if l == nil || !l.enabled || l.path == "" {
		return nil
	}

	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	// Model/Provider/Tool are display identifiers, but a key-shaped value
	// misdirected into a registry model id or provider name would otherwise
	// land raw in this durable 0600 line. MaskOperand is the same display
	// authority the report sinks use, so benign names stay readable while
	// credential-shaped ones render first-5+****.
	entry.Model = providers.MaskOperand(entry.Model)
	entry.Provider = providers.MaskOperand(entry.Provider)
	entry.Tool = providers.MaskOperand(entry.Tool)
	if l.redact != nil {
		entry.Arguments = l.redact(entry.Arguments)
		entry.Output = l.redact(entry.Output)
		if entry.Error != "" {
			entry.Error = l.redact(entry.Error)
		}
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()

	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(data)
	return err
}

// Path returns the configured log file path.
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
