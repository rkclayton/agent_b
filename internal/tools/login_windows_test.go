//go:build windows

package tools_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/windows"
	"harness/internal/agent"
	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/events"
	"harness/internal/projection"
	"harness/internal/recorder"
	"harness/internal/session"
	"harness/internal/tools"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func init() {
	if os.Getenv("AGENTB_SSH_ASKPASS_PIPE") != "" {
		for _, value := range append(os.Environ(), os.Args...) {
			if strings.Contains(value, "planted-login-secret-2yd") {
				os.Exit(3)
			}
		}
	}
	if tools.SSHAskpassMain() {
		os.Exit(0)
	}
	if os.Getenv("AGENTB_SSH_ASKPASS_PIPE") != "" && len(os.Args) == 2 {
		os.Exit(0)
	}
}

func TestShellStoredLogin2yd(t *testing.T) {
	for _, scenario := range []string{"match", "user", "host", "port", "refused", "host-key", "key-passphrase", "key", "forged", "phone", "resolved-host", "alias", "jump", "reflected", "scp", "sftp", "consent-once", "consent-deny", "consent-hangup"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			password := "planted-login-secret-2yd"
			var attempts atomic.Int32
			connections := make(chan net.Conn, 4)
			_, key, _ := ed25519.GenerateKey(rand.Reader)
			signer, _ := ssh.NewSignerFromKey(key)
			cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
				attempts.Add(1)
				t.Logf("password attempt bytes=%d matches=%t", len(p), string(p) == password)
				if scenario != "refused" && string(p) == password {
					return nil, nil
				}
				return nil, fmt.Errorf("refused")
			}}
			cfg.AddHostKey(signer)
			cfg.PublicKeyCallback = func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }
			address := "127.0.0.1:0"
			if scenario == "resolved-host" {
				address = "127.0.0.2:0"
			}
			listener, err := net.Listen("tcp", address)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				for {
					conn, e := listener.Accept()
					if e != nil {
						return
					}
					connections <- conn
					go func() {
						defer conn.Close()
						conn.SetDeadline(time.Now().Add(15 * time.Second))
						server, chans, requests, e := ssh.NewServerConn(conn, cfg)
						if e != nil {
							return
						}
						defer server.Close()
						go ssh.DiscardRequests(requests)
						for incoming := range chans {
							ch, requests, e := incoming.Accept()
							if e != nil {
								continue
							}
							go func() {
								defer ch.Close()
								for r := range requests {
									r.Reply(true, nil)
									if r.Type == "subsystem" {
										return
									}
									if r.Type == "exec" {
										if scenario == "reflected" {
											for _, b := range []byte(password) {
												ch.Write([]byte{b})
											}
										} else {
											ch.Write([]byte("ok\n"))
										}
										ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
										return
									}
								}
							}()
						}
					}()
				}
			}()
			port := listener.Addr().(*net.TCPAddr).Port
			if scenario == "resolved-host" {
				original, e := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
				if e != nil {
					t.Fatal(e)
				}
				defer original.Close()
				go func() {
					conn, e := original.Accept()
					if e != nil {
						return
					}
					defer conn.Close()
					conn.SetDeadline(time.Now().Add(15 * time.Second))
					server, _, _, e := ssh.NewServerConn(conn, cfg)
					if e == nil {
						server.Close()
					}
				}()
			}
			known := filepath.Join(root, "known_hosts")
			keyLine := fmt.Sprintf("[127.0.0.1]:%d %s", port, ssh.MarshalAuthorizedKey(signer.PublicKey()))
			if scenario == "resolved-host" {
				keyLine += fmt.Sprintf("[127.0.0.2]:%d %s", port, ssh.MarshalAuthorizedKey(signer.PublicKey()))
			}
			if scenario == "host-key" {
				keyLine = ""
			}
			os.WriteFile(known, []byte(keyLine), 0600)
			configPath := filepath.Join(root, "config")
			os.WriteFile(configPath, nil, 0600)
			vault := credential.NewVault(filepath.Join(root, "vault"))
			user, host, storedPort := "someone", "127.0.0.1", port
			if scenario == "user" {
				user = "someone-else"
			}
			if scenario == "host" {
				host = "localhost"
			}
			if scenario == "port" {
				storedPort++
			}
			if e := vault.PutLogin("example-login", user, host, fmt.Sprint(storedPort), password); e != nil {
				t.Fatal(e)
			}
			shell := tools.NewShell(config.Defaults(root).Shell)
			shell.SetLoginVault(vault)
			exe := filepath.Join(os.Getenv("WINDIR"), "System32", "OpenSSH", "ssh.exe")
			command := fmt.Sprintf("& '%s' -F '%s' -o 'UserKnownHostsFile=%s' -o 'GlobalKnownHostsFile=%s' -o IdentityAgent=none -o IdentityFile=none -o PubkeyAuthentication=no -o StrictHostKeyChecking=ask -p %d someone@127.0.0.1 echo ok", exe, configPath, known, known, port)
			if scenario == "resolved-host" {
				command = strings.Replace(command, " -F ", " -o HostName=127.0.0.2 -F ", 1)
			}
			if scenario == "alias" {
				os.WriteFile(configPath, []byte("Host example-alias\n HostName 127.0.0.1\n"), 0600)
				command = strings.Replace(command, "someone@127.0.0.1", "someone@example-alias", 1)
			}
			if scenario == "jump" {
				jump, e := net.Listen("tcp", "127.0.0.1:0")
				if e != nil {
					t.Fatal(e)
				}
				defer jump.Close()
				jumpPort := jump.Addr().(*net.TCPAddr).Port
				os.WriteFile(known, []byte(keyLine+fmt.Sprintf("[127.0.0.1]:%d %s", jumpPort, ssh.MarshalAuthorizedKey(signer.PublicKey()))), 0600)
				os.WriteFile(configPath, []byte(fmt.Sprintf("Host fixture-jump\n HostName 127.0.0.1\n Port %d\n User someone\n IdentityAgent none\n IdentityFile none\n UserKnownHostsFile %s\n GlobalKnownHostsFile %s\n StrictHostKeyChecking yes\n", jumpPort, known, known)), 0600)
				go func() {
					conn, e := jump.Accept()
					if e != nil {
						return
					}
					defer conn.Close()
					conn.SetDeadline(time.Now().Add(20 * time.Second))
					jumpConfig := &ssh.ServerConfig{NoClientAuth: true}
					jumpConfig.AddHostKey(signer)
					server, channels, requests, e := ssh.NewServerConn(conn, jumpConfig)
					if e != nil {
						return
					}
					defer server.Close()
					go ssh.DiscardRequests(requests)
					for incoming := range channels {
						if incoming.ChannelType() != "direct-tcpip" {
							incoming.Reject(ssh.UnknownChannelType, "forward only")
							continue
						}
						channel, requests, e := incoming.Accept()
						if e != nil {
							return
						}
						go ssh.DiscardRequests(requests)
						destination, e := net.Dial("tcp", listener.Addr().String())
						if e != nil {
							channel.Close()
							return
						}
						go func() {
							defer destination.Close()
							defer channel.Close()
							go io.Copy(channel, destination)
							io.Copy(destination, channel)
						}()
					}
				}()
				command = strings.Replace(command, " -F ", " -J fixture-jump -F ", 1)
			}
			if scenario == "key-passphrase" || scenario == "key" {
				_, private, _ := ed25519.GenerateKey(rand.Reader)
				block, e := ssh.MarshalPrivateKeyWithPassphrase(private, "fixture", []byte("fixture-passphrase"))
				if scenario == "key" {
					block, e = ssh.MarshalPrivateKey(private, "fixture")
				}
				if e != nil {
					t.Fatal(e)
				}
				path := filepath.Join(root, "fixture-key")
				os.WriteFile(path, pem.EncodeToMemory(block), 0600)
				user, e := windows.GetCurrentProcessToken().GetTokenUser()
				if e != nil {
					t.Fatal(e)
				}
				sd, e := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")
				if e != nil {
					t.Fatal(e)
				}
				acl, _, e := sd.DACL()
				if e != nil {
					t.Fatal(e)
				}
				if e = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); e != nil {
					t.Fatal(e)
				}
				command = strings.Replace(command, "-o PubkeyAuthentication=no", "-o PubkeyAuthentication=yes -o IdentitiesOnly=yes -i '"+path+"'", 1)
			}
			if scenario == "scp" || scenario == "sftp" {
				exe = filepath.Join(filepath.Dir(exe), scenario+".exe")
				command = fmt.Sprintf("& '%s' -F '%s' -o 'UserKnownHostsFile=%s' -o 'GlobalKnownHostsFile=%s' -o IdentityAgent=none -o IdentityFile=none -o PubkeyAuthentication=no -o StrictHostKeyChecking=yes -P %d someone@127.0.0.1", exe, configPath, known, known, port)
				if scenario == "scp" {
					command += "':/fixture' '" + filepath.Join(root, "received") + "'"
				}
			}
			if scenario == "forged" {
				helper, _ := os.Executable()
				command = fmt.Sprintf("& '%s' \"someone@127.0.0.1's password: \"", helper)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if scenario == "phone" {
				ctx = tools.WithLoginSource(ctx, "phone")
			}
			var observed strings.Builder
			ctx = tools.WithOutputObserver(ctx, func(line string) { observed.WriteString(line) })
			item := &session.Session{ID: "chat", Workspace: root, ToolsEnabled: map[string]bool{"shell": true}, Run: session.RunState{Status: "running"}}
			if strings.HasPrefix(scenario, "consent-") {
				gateConfig := config.Defaults(root)
				gateConfig.Credentials.AskBeforeUse = true
				bus := events.NewBus()
				evs, unsub := bus.Subscribe()
				defer unsub()
				gate := agent.NewGate(bus, func() config.Config { return gateConfig })
				ctx = tools.WithCredentialApproval(ctx, func(wait context.Context, entry credential.Entry, to string) error {
					return gate.WaitCredential(wait, item, "run", "call", entry, to, command)
				})
				go func() {
					for {
						select {
						case ev := <-evs:
							if ev.Type != events.ApprovalRequired {
								continue
							}
							if vault.SecretReads() != 0 {
								t.Error("login read before approval")
							}
							decision := "once"
							if scenario == "consent-deny" {
								decision = "deny"
							}
							if scenario == "consent-hangup" {
								(<-connections).Close()
								time.Sleep(6 * time.Second)
								decision = "session"
							}
							if err := gate.Decide(item.ID, "call", decision); err != nil {
								t.Error(err)
							}
							return
						case <-ctx.Done():
							return
						}
					}
				}()
			}
			registry := tools.New(shell)
			outcome := registry.CallDetailed(ctx, item, "shell", map[string]any{"command": command, "timeout_s": 15})
			if scenario == "consent-hangup" {
				if !strings.Contains(outcome.Content, "SSH connection closed while waiting for credential approval") {
					t.Fatalf("missing hangup: %s", outcome.Content)
				}
				outcome = registry.CallDetailed(ctx, item, "shell", map[string]any{"command": command, "timeout_s": 15})
				if !outcome.OK {
					t.Fatalf("retry after chat approval: %s", outcome.Content)
				}
			}
			if scenario == "consent-deny" && (!strings.Contains(outcome.Content, "credential example-login not approved") || vault.SecretReads() != 0) {
				t.Fatalf("denial read or missing: %s", outcome.Content)
			}
			result := tools.CallDetail{Content: outcome.Content, Metadata: outcome.Metadata}
			if !outcome.OK {
				result.Err = errors.New("tool failed")
			}
			text := fmt.Sprintf("%s %v %v %s", result.Content, result.Err, result.Metadata, observed.String())
			if scenario == "reflected" {
				writers, e := events.NewWriters(filepath.Join(root, "logs"))
				if e != nil {
					t.Fatal(e)
				}
				if _, e = writers.OpenSession("s1"); e != nil {
					t.Fatal(e)
				}
				project := projection.NewStore()
				record := recorder.New()
				record.Settle = time.Millisecond
				sent := make(chan []byte, 8)
				record.SetSink(func(_ string, data map[string]any) { encoded, _ := json.Marshal(data); sent <- encoded })
				data := map[string]any{"turn": 1, "call_id": "c1", "name": "shell", "ok": outcome.OK, "preview": outcome.Content, "bytes": len(outcome.Content)}
				for key, value := range outcome.Metadata {
					data[key] = value
				}
				for _, event := range []events.Event{events.New(events.RunStarted, "s1", "r1", map[string]any{"run_id": "r1"}), events.New(events.ToolCallEvent, "s1", "r1", map[string]any{"call_id": "c1", "name": "shell", "args": map[string]any{"command": command}}), events.New(events.ToolResult, "s1", "r1", data), events.New(events.MessageAppended, "s1", "r1", map[string]any{"message": events.Message{ID: "m1", Role: "tool", Name: "shell", ToolCallID: "c1", Content: outcome.Content}}), events.New(events.RunStopped, "s1", "r1", map[string]any{"reason": "done"})} {
					cursor, e := writers.WriteRecord(event)
					if e != nil {
						t.Fatal(e)
					}
					project.Apply(event, cursor)
					record.Observe(event)
				}
				writers.Close()
				snapshot, e := project.Snapshot(project.Sources())
				if e != nil {
					t.Fatal(e)
				}
				phone, _ := json.Marshal(snapshot)
				if !strings.Contains(string(phone), "[redacted]") {
					t.Fatal("phone projection did not carry the exercised result")
				}
				if strings.Contains(string(phone), password) {
					t.Fatal("password in phone projection")
				}
				select {
				case telemetry := <-sent:
					if strings.Contains(string(telemetry), password) {
						t.Fatal("password in telemetry")
					}
				case <-time.After(time.Second):
					t.Fatal("no telemetry summary")
				}
				filepath.Walk(filepath.Join(root, "logs"), func(path string, info os.FileInfo, e error) error {
					if e != nil {
						t.Fatal(e)
					}
					if !info.IsDir() {
						data, e := os.ReadFile(path)
						if e != nil {
							t.Fatal(e)
						}
						if strings.Contains(string(data), password) {
							t.Fatal("password in journal or global log")
						}
					}
					return nil
				})
			}
			if strings.Contains(text, password) {
				t.Fatal("secret escaped")
			}
			want := int32(0)
			if scenario == "phone" || scenario == "alias" || scenario == "jump" || scenario == "match" || scenario == "refused" || scenario == "reflected" || scenario == "scp" || scenario == "sftp" || scenario == "consent-once" || scenario == "consent-hangup" {
				want = 1
			}
			if attempts.Load() != want {
				t.Fatalf("attempts=%d want=%d result=%s", attempts.Load(), want, text)
			}
			if (scenario == "phone" || scenario == "alias" || scenario == "jump" || scenario == "match") && (result.Err != nil || !strings.Contains(text, "ok") || !strings.Contains(text, "login: example-login")) {
				t.Fatal(text)
			}
			if scenario == "resolved-host" && !strings.Contains(text, "no stored login for someone@127.0.0.2") {
				t.Fatal(text)
			}
			if scenario == "forged" && result.Err != nil {
				t.Fatal("ordinary single-argument launch failed", result.Err)
			}
			if scenario == "key" && (result.Err != nil || !strings.Contains(text, "ok") || strings.Contains(text, "login:")) {
				t.Fatal(text)
			}
			if scenario == "reflected" && (result.Err != nil || !strings.Contains(text, "[redacted]")) {
				t.Fatal(text)
			}
			if scenario == "refused" && !strings.Contains(text, "stored login for someone@127.0.0.1 was refused") {
				t.Fatal(text)
			}
			if scenario == "user" || scenario == "host" || scenario == "port" {
				if !strings.Contains(text, "no stored login for someone@127.0.0.1") {
					t.Fatal(text)
				}
			}
		})
	}
}

func TestLoginOutputBound2yd(t *testing.T) {
	root := t.TempDir()
	shell := tools.NewShell(config.Defaults(root).Shell)
	vault := credential.NewVault(filepath.Join(root, "vault"))
	if err := vault.PutLogin("example-login", "someone", "example.org", "22", "fixture-password"); err != nil {
		t.Fatal(err)
	}
	shell.SetLoginVault(vault)
	start := time.Now()
	out := shell.CallDetailed(context.Background(), &session.Session{Workspace: root}, map[string]any{"command": "[Console]::Write(('x' * (2 * 1024 * 1024)))"})
	if out.Err != nil || !strings.Contains(out.Content, "1048576 output bytes omitted") || len(out.Content) > (1<<20)+256 {
		t.Fatal("output bound", out.Err, len(out.Content))
	}
	t.Logf("2 MiB output retained at 1 MiB bound in %s", time.Since(start))
}

func TestEmptyLoginVaultKeepsOrdinaryShell2yl(t *testing.T) {
	for _, key := range []string{"SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "AGENTB_SSH_ASKPASS_PIPE"} {
		t.Setenv(key, "")
	}
	root := t.TempDir()
	shell := tools.NewShell(config.Defaults(root).Shell)
	item := &session.Session{Workspace: root}
	call := func(command string) string {
		out := shell.CallDetailed(context.Background(), item, map[string]any{"command": command})
		if out.Err != nil {
			t.Fatal(out.Err)
		}
		return out.Content
	}
	command := "[Console]::Write(('x' * (3 * 1024 * 1024)))"
	baseline := call(command)
	vault := credential.NewVault(filepath.Join(root, "vault"))
	shell.SetLoginVault(vault)
	if got := call(command); got != baseline {
		t.Fatalf("empty vault changed 3 MiB output: %d versus %d bytes", len(got), len(baseline))
	}
	environment := "[Console]::Write(\"$env:SSH_ASKPASS|$env:SSH_ASKPASS_REQUIRE|$env:AGENTB_SSH_ASKPASS_PIPE\")"
	if got := call(environment); strings.Contains(got, "AgentB-login") || strings.Contains(got, "force") {
		t.Fatal("empty vault armed helper")
	}
	if err := vault.PutLogin("example-login", "someone", "example.org", "22", "fixture-password"); err != nil {
		t.Fatal(err)
	}
	if got := call(environment); !strings.Contains(got, "AgentB-login") || !strings.Contains(got, "force") {
		t.Fatal("stored login did not arm helper")
	}
	if err := vault.Delete("example-login"); err != nil {
		t.Fatal(err)
	}
	if got := call(environment); strings.Contains(got, "AgentB-login") || strings.Contains(got, "force") {
		t.Fatal("removed last login left helper armed")
	}
	t.Logf("3 MiB output exactly matches ordinary shell: %d returned bytes", len(baseline))
}
