package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"
)

// A recording saves the raw game packets to a file, so they can be looked
// at later to find new things to read (like your carry weight).
//
// File layout, repeated for every packet:
//
//	8 bytes  time (Unix nanoseconds)
//	1 byte   1 = from the game server, 0 = from your PC
//	4 bytes  length
//	...      the packet

type Packet struct {
	Data       []byte
	FromServer bool
}

type Recorder struct {
	file    *os.File
	w       *bufio.Writer
	Path    string
	Until   time.Time
	Packets int
}

func startRecording(seconds int) (*Recorder, error) {
	path := "recording-" + time.Now().Format("2006-01-02-150405") + ".albrec"
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &Recorder{
		file: f, w: bufio.NewWriter(f), Path: path,
		Until: time.Now().Add(time.Duration(seconds) * time.Second),
	}, nil
}

func (r *Recorder) write(p Packet) {
	var head [13]byte
	binary.BigEndian.PutUint64(head[0:], uint64(time.Now().UnixNano()))
	if p.FromServer {
		head[8] = 1
	}
	binary.BigEndian.PutUint32(head[9:], uint32(len(p.Data)))
	r.w.Write(head[:])
	r.w.Write(p.Data)
	r.Packets++
}

func (r *Recorder) close() {
	r.w.Flush()
	r.file.Close()
}

// readRecording plays a recording back, one packet at a time.
func readRecording(path string, each func(p Packet, at time.Time)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		var head [13]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		n := binary.BigEndian.Uint32(head[9:])
		if n > 1<<20 {
			return fmt.Errorf("broken recording: packet of %d bytes", n)
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(r, data); err != nil {
			return err
		}
		at := time.Unix(0, int64(binary.BigEndian.Uint64(head[0:])))
		each(Packet{Data: data, FromServer: head[8] == 1}, at)
	}
}

// --- the App side: start, stop, and pass packets in ---

type recordingJSON struct {
	Active      bool   `json:"active"`
	SecondsLeft int    `json:"secondsLeft"`
	Packets     int    `json:"packets"`
	File        string `json:"file"`
}

func (a *App) startRecording(seconds int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rec != nil {
		return nil
	}
	r, err := startRecording(seconds)
	if err != nil {
		return err
	}
	a.rec = r
	a.lastRec = recordingJSON{}
	go func() {
		time.Sleep(time.Until(r.Until))
		a.stopRecording(r)
	}()
	return nil
}

// stopRecording stops the current recording. If only is set, it stops
// only that one (so an old timer can't stop a newer recording).
func (a *App) stopRecording(only *Recorder) {
	a.mu.Lock()
	r := a.rec
	if only != nil && r != only {
		a.mu.Unlock()
		return
	}
	a.rec = nil
	if r != nil {
		r.close()
		a.lastRec = recordingJSON{Packets: r.Packets, File: r.Path}
	}
	a.mu.Unlock()
	if r != nil {
		a.event("zone", "Saved %d packets to %s", r.Packets, r.Path)
	}
}

func (a *App) recordPacket(p Packet) {
	a.mu.Lock()
	if a.rec != nil {
		a.rec.write(p)
	}
	a.mu.Unlock()
}

func (a *App) recordingState() recordingJSON {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rec == nil {
		return a.lastRec
	}
	left := int(time.Until(a.rec.Until).Seconds())
	if left < 0 {
		left = 0
	}
	return recordingJSON{Active: true, SecondsLeft: left, Packets: a.rec.Packets, File: a.rec.Path}
}

// dumpRecording prints every message in a recording, for finding new
// things to read. Run it with: go run . -replay recording-....albrec
func dumpRecording(path string, app *App) error {
	photon := NewPhoton()
	var at time.Time
	var fromServer bool
	photon.OnMessage = func(m Message) {
		dir := "PC->server"
		if fromServer {
			dir = "server->PC"
		}
		kinds := map[byte]string{msgRequest: "request", msgResponse: "response", msgEvent: "event"}
		fmt.Printf("%s %s %s code=%d", at.Format("15:04:05.000"), dir, kinds[m.Kind], m.Code)
		if op, ok := m.Params[253]; ok {
			fmt.Printf(" op=%v", op)
		}
		if ev, ok := m.Params[252]; ok {
			fmt.Printf(" event=%v", ev)
		}
		if m.Extra != nil {
			fmt.Printf(" extra=%s", short(m.Extra))
		}
		for k := 0; k < 256; k++ {
			if v, ok := m.Params[byte(k)]; ok && k != 252 && k != 253 {
				fmt.Printf(" [%d]=%s", k, short(v))
			}
		}
		fmt.Println()
		if app != nil {
			app.handle(m)
		}
	}
	photon.OnEncrypted = func() { fmt.Println(at.Format("15:04:05.000"), "ENCRYPTED") }
	return readRecording(path, func(p Packet, t time.Time) {
		at, fromServer = t, p.FromServer
		photon.Feed(p.Data)
	})
}

func short(v any) string {
	s := fmt.Sprintf("%v", v)
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}

// findInRecording prints every message that contains a number close to
// target, e.g. the carry weight you saw in game.
func findInRecording(path string, target float64) error {
	photon := NewPhoton()
	var at time.Time
	photon.OnMessage = func(m Message) {
		var hits []string
		for k, v := range m.Params {
			if containsNumber(v, target) {
				hits = append(hits, fmt.Sprintf("[%d]=%s", k, short(v)))
			}
		}
		if containsNumber(m.Extra, target) {
			hits = append(hits, "extra="+short(m.Extra))
		}
		if len(hits) > 0 {
			fmt.Printf("%s kind=%d code=%d op=%v event=%v %v\n",
				at.Format("15:04:05.000"), m.Kind, m.Code, m.Params[253], m.Params[252], hits)
		}
	}
	return readRecording(path, func(p Packet, t time.Time) {
		at = t
		photon.Feed(p.Data)
	})
}

func containsNumber(v any, target float64) bool {
	near := func(f float64) bool {
		d := f - target
		return d > -0.6 && d < 0.6
	}
	switch x := v.(type) {
	case int64:
		return near(float64(x)) || near(float64(x)/10000)
	case int16:
		return near(float64(x))
	case byte:
		return near(float64(x))
	case float32:
		return near(float64(x))
	case float64:
		return near(x)
	case []any:
		for _, e := range x {
			if containsNumber(e, target) {
				return true
			}
		}
	case map[any]any:
		for _, e := range x {
			if containsNumber(e, target) {
				return true
			}
		}
	case map[byte]any:
		for _, e := range x {
			if containsNumber(e, target) {
				return true
			}
		}
	}
	return false
}
