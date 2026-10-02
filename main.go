// timer serves a full-screen interval timer in the browser.
//
//	go run .                      # default schedule
//	go run . -schedule my.json    # custom schedule
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
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
	return &s, nil
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
