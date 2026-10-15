package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const browserIdle = 10 * time.Minute

type browserState struct{ PID, Port int }
type browserSocket struct {
	net.Conn
	reader *bufio.Reader
	id     int
}

func browserUsage() string {
	return "usage: browser start | stop | nav <url> | eval <js> | text | screenshot | click <selector|text> | fill <selector|text> <value> | press <key> | wait <selector|text|ms> | pick <text>"
}

func runBrowserCommand(args []string) int {
	if len(args) == 0 {
		fmt.Println(browserUsage())
		return 0
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if args[0] == "watch" && len(args) == 2 {
		browserWatch(args[1])
		return 0
	}
	statePath, profile := browserPaths(cwd)
	if args[0] == "start" {
		if err := startBrowser(statePath, profile, cwd); err != nil {
			fmt.Fprintln(os.Stderr, "browser:", err)
			return 1
		}
		fmt.Println("browser started — about:blank")
		return 0
	}
	state, err := readBrowserState(statePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser: not started; run browser start")
		return 1
	}
	if args[0] == "stop" {
		stopBrowser(statePath, profile, state)
		fmt.Println("browser stopped")
		return 0
	}
	_ = os.Chtimes(statePath, time.Now(), time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	socket, err := connectBrowser(ctx, state.Port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser:", err)
		return 1
	}
	defer socket.Close()
	if err := browserVerb(ctx, socket, cwd, args); err != nil {
		fmt.Fprintln(os.Stderr, "browser:", err)
		return 1
	}
	return 0
}

func stopBrowser(statePath, profile string, state browserState) {
	killBrowserProcess(state.PID)
	_ = os.Remove(statePath)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := os.RemoveAll(profile); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func browserPaths(cwd string) (string, string) {
	hash := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(cwd))))
	root := filepath.Join(os.TempDir(), "agentb-browser", hex.EncodeToString(hash[:8]))
	return filepath.Join(root, "state.json"), filepath.Join(root, "profile")
}

func startBrowser(statePath, profile, cwd string) error {
	if prior, err := readBrowserState(statePath); err == nil {
		killBrowserProcess(prior.PID)
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		return err
	}
	if err := os.RemoveAll(profile); err != nil {
		return err
	}
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return err
	}
	edge, err := findEdge()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	pid, err := startBrowserDetached(edge, []string{"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--remote-debugging-port=" + strconv.Itoa(port), "--remote-debugging-address=127.0.0.1", "--user-data-dir=" + profile, "--window-size=1280,800", "--disk-cache-size=67108864", "--media-cache-size=16777216", "about:blank"}, cwd)
	if err != nil {
		return err
	}
	state := browserState{PID: pid, Port: port}
	encoded, _ := json.Marshal(state)
	if err := os.WriteFile(statePath, encoded, 0o600); err != nil {
		killBrowserProcess(pid)
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err = browserPageURL(ctx, port)
		cancel()
		if err == nil {
			if !strings.HasSuffix(strings.ToLower(os.Args[0]), ".test.exe") {
				_, _ = startBrowserDetached(os.Args[0], []string{"browser", "watch", statePath}, cwd)
			}
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	killBrowserProcess(pid)
	return errors.New("Edge did not open its debugging endpoint")
}

func browserWatch(statePath string) {
	profile := filepath.Join(filepath.Dir(statePath), "profile")
	for {
		info, err := os.Stat(statePath)
		if err != nil {
			return
		}
		if time.Since(info.ModTime()) >= browserIdle {
			if state, err := readBrowserState(statePath); err == nil {
				killBrowserProcess(state.PID)
			}
			_ = os.Remove(statePath)
			_ = os.RemoveAll(profile)
			return
		}
		time.Sleep(5 * time.Second)
	}
}

func readBrowserState(path string) (browserState, error) {
	var state browserState
	data, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(data, &state)
	if state.PID <= 0 || state.Port <= 0 {
		return state, errors.New("invalid browser state")
	}
	return state, err
}

func findEdge() (string, error) {
	for _, candidate := range []string{filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"), filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"), "msedge.exe"} {
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved, nil
		}
	}
	return "", errors.New("Microsoft Edge is not installed")
}

func browserPageURL(ctx context.Context, port int) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/list", port), nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var pages []struct {
		Type      string `json:"type"`
		WebSocket string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&pages); err != nil {
		return "", err
	}
	for _, page := range pages {
		if page.Type == "page" && page.WebSocket != "" {
			return page.WebSocket, nil
		}
	}
	return "", errors.New("Edge exposed no page")
}

func connectBrowser(ctx context.Context, port int) (*browserSocket, error) {
	address, err := browserPageURL(ctx, port)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, err
	}
	keyBytes := make([]byte, 16)
	_, _ = rand.Read(keyBytes)
	key := base64.StdEncoding.EncodeToString(keyBytes)
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", u.RequestURI(), u.Host, key)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, fmt.Errorf("debug websocket returned %s", response.Status)
	}
	return &browserSocket{Conn: conn, reader: reader}, nil
}

func (s *browserSocket) call(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	s.id++
	request, _ := json.Marshal(map[string]any{"id": s.id, "method": method, "params": params})
	if deadline, ok := ctx.Deadline(); ok {
		_ = s.SetDeadline(deadline)
	}
	if err := s.writeFrame(1, request); err != nil {
		return nil, err
	}
	for {
		payload, opcode, err := s.readFrame()
		if err != nil {
			return nil, err
		}
		if opcode == 9 {
			_ = s.writeFrame(10, payload)
			continue
		}
		if opcode != 1 {
			continue
		}
		var response map[string]any
		if json.Unmarshal(payload, &response) != nil || int(numberValue(response["id"])) != s.id {
			continue
		}
		if failure, ok := response["error"].(map[string]any); ok {
			return nil, fmt.Errorf("%v", failure["message"])
		}
		result, _ := response["result"].(map[string]any)
		return result, nil
	}
}

func (s *browserSocket) writeFrame(opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, 0x80|byte(length))
	case length <= 65535:
		header = append(header, 0x80|126, byte(length>>8), byte(length))
	default:
		header = append(header, 0x80|127, 0, 0, 0, 0, byte(uint64(length)>>24), byte(uint64(length)>>16), byte(uint64(length)>>8), byte(length))
	}
	mask := make([]byte, 4)
	_, _ = rand.Read(mask)
	header = append(header, mask...)
	masked := make([]byte, length)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	_, err := s.Write(append(header, masked...))
	return err
}

func (s *browserSocket) readFrame() ([]byte, byte, error) {
	first, err := s.reader.ReadByte()
	if err != nil {
		return nil, 0, err
	}
	second, err := s.reader.ReadByte()
	if err != nil {
		return nil, 0, err
	}
	length := uint64(second & 0x7f)
	if length == 126 {
		var value uint16
		if err := binary.Read(s.reader, binary.BigEndian, &value); err != nil {
			return nil, 0, err
		}
		length = uint64(value)
	}
	if length == 127 {
		if err := binary.Read(s.reader, binary.BigEndian, &length); err != nil {
			return nil, 0, err
		}
	}
	if length > 16<<20 {
		return nil, 0, errors.New("browser response exceeds 16 MiB")
	}
	payload := make([]byte, int(length))
	_, err = io.ReadFull(s.reader, payload)
	return payload, first & 0x0f, err
}

func browserEval(ctx context.Context, socket *browserSocket, expression string) (any, error) {
	result, err := socket.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true})
	if err != nil {
		return nil, err
	}
	remote, _ := result["result"].(map[string]any)
	if description, _ := remote["description"].(string); remote["subtype"] == "error" || strings.Contains(description, "Error") {
		return nil, errors.New(description)
	}
	return remote["value"], nil
}

func pageFacts(ctx context.Context, socket *browserSocket) (string, error) {
	value, err := browserEval(ctx, socket, `({title:document.title,address:location.href})`)
	if err != nil {
		return "", err
	}
	object, _ := value.(map[string]any)
	return fmt.Sprintf("title=%q address=%s", object["title"], object["address"]), nil
}

func targetScript(target, action, value string) string {
	t, _ := json.Marshal(target)
	v, _ := json.Marshal(value)
	return fmt.Sprintf(`(()=>{const q=%s;let e=null;try{e=document.querySelector(q)}catch{}if(!e)e=[...document.querySelectorAll('body *')].find(x=>(x.innerText||'').trim()===q)||[...document.querySelectorAll('body *')].find(x=>(x.innerText||'').includes(q));if(!e)throw new Error('target not found: '+q);%s;return {acted:(e.tagName.toLowerCase()+(e.id?'#'+e.id:'')),title:document.title,address:location.href}})()`, t, strings.ReplaceAll(action, "$VALUE", string(v)))
}

func browserVerb(ctx context.Context, socket *browserSocket, cwd string, args []string) error {
	verb := args[0]
	switch verb {
	case "nav":
		if len(args) != 2 {
			return errors.New(browserUsage())
		}
		u, err := url.Parse(args[1])
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("nav requires an http or https address")
		}
		if _, err = socket.call(ctx, "Page.navigate", map[string]any{"url": u.String()}); err != nil {
			return err
		}
		for {
			value, _ := browserEval(ctx, socket, `document.readyState`)
			if value == "complete" {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
		facts, err := pageFacts(ctx, socket)
		if err == nil {
			fmt.Println("navigated — " + facts)
		}
		return err
	case "eval":
		if len(args) < 2 {
			return errors.New(browserUsage())
		}
		value, err := browserEval(ctx, socket, strings.Join(args[1:], " "))
		if err != nil {
			return err
		}
		encoded, _ := json.Marshal(value)
		fmt.Println(string(encoded))
		return nil
	case "text":
		value, err := browserEval(ctx, socket, `document.body?document.body.innerText:""`)
		if err != nil {
			return err
		}
		text := fmt.Sprint(value)
		if len(text) > 65536 {
			text = text[:65536] + "\n[cut to 65,536 bytes]"
		}
		fmt.Println(text)
		return nil
	case "screenshot":
		result, err := socket.call(ctx, "Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": false})
		if err != nil {
			return err
		}
		data, err := base64.StdEncoding.DecodeString(fmt.Sprint(result["data"]))
		if err != nil {
			return err
		}
		directory := filepath.Join(cwd, "screenshots")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
		name := filepath.Join(directory, "browser-"+time.Now().UTC().Format("20060102-150405.000")+".png")
		if err := os.WriteFile(name, data, 0o600); err != nil {
			return err
		}
		relative, _ := filepath.Rel(cwd, name)
		fmt.Println("browser screenshot: " + filepath.ToSlash(relative))
		return nil
	case "click", "pick":
		if len(args) < 2 {
			return errors.New(browserUsage())
		}
		value, err := browserEval(ctx, socket, targetScript(strings.Join(args[1:], " "), `e.click()`, ""))
		if err != nil {
			return err
		}
		object, _ := value.(map[string]any)
		fmt.Printf("acted on %s — title=%q address=%s\n", object["acted"], object["title"], object["address"])
		return nil
	case "fill":
		if len(args) != 3 {
			return errors.New(browserUsage())
		}
		value, err := browserEval(ctx, socket, targetScript(args[1], `e.focus();e.value=$VALUE;e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}))`, args[2]))
		if err != nil {
			return err
		}
		object, _ := value.(map[string]any)
		fmt.Printf("acted on %s — title=%q address=%s\n", object["acted"], object["title"], object["address"])
		return nil
	case "press":
		if len(args) != 2 {
			return errors.New(browserUsage())
		}
		for _, kind := range []string{"keyDown", "keyUp"} {
			if _, err := socket.call(ctx, "Input.dispatchKeyEvent", map[string]any{"type": kind, "key": args[1], "code": args[1]}); err != nil {
				return err
			}
		}
		facts, err := pageFacts(ctx, socket)
		if err == nil {
			fmt.Printf("pressed %s — %s\n", args[1], facts)
		}
		return err
	case "wait":
		if len(args) < 2 {
			return errors.New(browserUsage())
		}
		target := strings.Join(args[1:], " ")
		if ms, err := strconv.Atoi(target); err == nil {
			if ms < 0 || ms > 60000 {
				return errors.New("wait milliseconds must be from 0 to 60000")
			}
			time.Sleep(time.Duration(ms) * time.Millisecond)
		} else {
			deadline := time.Now().Add(60 * time.Second)
			for {
				found, _ := browserEval(ctx, socket, targetScript(target, ``, ""))
				if found != nil {
					break
				}
				if time.Now().After(deadline) {
					return errors.New("wait target timed out")
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		facts, err := pageFacts(ctx, socket)
		if err == nil {
			fmt.Printf("waited for %s — %s\n", target, facts)
		}
		return err
	default:
		return errors.New(browserUsage())
	}
}

func numberValue(value any) float64 { number, _ := value.(float64); return number }
