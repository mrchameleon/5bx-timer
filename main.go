// timer serves a full-screen interval timer in the browser.
//
//	go run .                      # default schedule
//	go run . -schedule my.json    # custom schedule
//
// Settings and "where you left off" are saved to state.json in the user config
// dir (e.g. ~/Library/Application Support/interval-timer on macOS).
package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

//go:embed index.html
var indexHTML []byte

//go:embed schedule.json
var defaultSchedule []byte

// Phase is one timed block. Kind controls color and sounds: "prep", "work", "rest".
type Phase struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Seconds int    `json:"seconds"`
}

type Schedule struct {
	// ID identifies the phase list, so saved progress is only resumed on the same schedule.
	ID     string  `json:"id"`
	Voice  bool    `json:"voice"`
	Phases []Phase `json:"phases"`
}

func main() {
	schedPath := flag.String("schedule", "", "path to a schedule JSON file (default: built-in schedule)")
	port := flag.Int("port", 0, "port to listen on (default: random free port)")
	noOpen := flag.Bool("no-open", false, "don't open the browser automatically")
	flag.Parse()

	sched, err := loadSchedule(*schedPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	schedJSON, _ := json.Marshal(sched)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	mux.HandleFunc("/schedule.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(schedJSON)
	})
	mux.HandleFunc("/state", stateHandler(statePath()))

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: can't listen on port %d: %v\nTry a different -port, or omit it to pick a free one.\n", *port, err)
		os.Exit(1)
	}
	url := "http://" + ln.Addr().String()

	total := 0
	for _, p := range sched.Phases {
		total += p.Seconds
	}
	fmt.Printf("Timer running at %s  (%d phases, %d:%02d total)\n", url, len(sched.Phases), total/60, total%60)
	fmt.Println("Press Ctrl+C to quit.")

	if !*noOpen {
		if err := openBrowser(url); err != nil {
			fmt.Printf("Couldn't open a browser automatically (%v). Open %s yourself.\n", err, url)
		}
	}
	log.Fatal(http.Serve(ln, mux))
}

func loadSchedule(path string) (*Schedule, error) {
	data := defaultSchedule
	src := "built-in schedule"
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("can't read schedule: %w", err)
		}
		data, src = b, path
	}
	var s Schedule
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", src, err)
	}
	if len(s.Phases) == 0 {
		return nil, fmt.Errorf("%s has no phases", src)
	}
	for i, p := range s.Phases {
		if p.Seconds <= 0 {
			return nil, fmt.Errorf("%s: phase %d (%q) needs \"seconds\" > 0", src, i+1, p.Name)
		}
		switch p.Kind {
		case "prep", "work", "rest":
		default:
			return nil, fmt.Errorf("%s: phase %d (%q) has kind %q; use \"prep\", \"work\" or \"rest\"", src, i+1, p.Name, p.Kind)
		}
	}
	hasWork := false
	for _, p := range s.Phases {
		hasWork = hasWork || p.Kind == "work"
	}
	if !hasWork {
		return nil, fmt.Errorf("%s has no \"work\" phases", src)
	}
	phasesJSON, _ := json.Marshal(s.Phases)
	sum := sha256.Sum256(phasesJSON)
	s.ID = hex.EncodeToString(sum[:6])
	return &s, nil
}

// statePath is where the browser's settings and progress are kept. They can't live in
// localStorage: the port changes between runs, and each port gets its own storage.
func statePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "interval-timer", "state.json")
}

// stateHandler stores one small JSON object for the page: GET reads it, PUT replaces it.
func stateHandler(path string) http.HandlerFunc {
	var mu sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			b, err := os.ReadFile(path)
			var obj map[string]json.RawMessage
			if err != nil || json.Unmarshal(b, &obj) != nil {
				b = []byte("{}")
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(b)
		case http.MethodPut:
			b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
			var obj map[string]json.RawMessage
			if err != nil || json.Unmarshal(b, &obj) != nil {
				http.Error(w, "state must be a JSON object under 64 KB", http.StatusBadRequest)
				return
			}
			if err := writeFileAtomic(path, b); err != nil {
				log.Printf("can't save progress to %s: %v", path, err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		default:
			w.Header().Set("Allow", "GET, PUT")
			http.Error(w, "use GET or PUT", http.StatusMethodNotAllowed)
		}
	}
}

func writeFileAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
