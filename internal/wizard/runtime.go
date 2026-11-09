package wizard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lukelex/dotpkg/internal/reconcile"
)

type Options struct {
	Reconcile      reconcile.Options
	Plain          bool
	Yes            bool
	Resume         bool
	PackagesOnly   bool
	SelectionsPath string
}

type Session struct {
	Profile    string            `json:"profile"`
	Host       string            `json:"host"`
	Selections map[string]bool   `json:"selections"`
	Inputs     map[string]string `json:"inputs"`
	Deferred   map[string]bool   `json:"deferred"`
	Confirmed  map[string]bool   `json:"confirmed"`
}

type runtime struct {
	options    Options
	spec       Spec
	session    Session
	directory  string
	executable string
	log        *os.File
	events     *os.File
	mu         sync.Mutex
	activity   string
}

func (r *runtime) sessionPath() string    { return filepath.Join(r.directory, "session.json") }
func (r *runtime) selectionsPath() string { return filepath.Join(r.directory, "selections.json") }

func atomicJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".wizard-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (r *runtime) save() error {
	if err := atomicJSON(r.sessionPath(), r.session); err != nil {
		return err
	}
	return atomicJSON(r.selectionsPath(), r.session.Selections)
}

func (r *runtime) values() map[string]string {
	values := map[string]string{
		"root": r.options.Reconcile.RootPath, "profile": r.session.Profile,
		"host": r.session.Host, "manifest": r.options.Reconcile.ManifestPath,
		"binary": r.executable, "selections": r.selectionsPath(),
	}
	for key, value := range r.session.Inputs {
		values[key] = value
	}
	return values
}

func (r *runtime) expand(value string) string {
	values := r.values()
	return os.Expand(value, func(key string) string {
		if value, ok := values[key]; ok {
			return value
		}
		return os.Getenv(key)
	})
}

func (r *runtime) command(ctx context.Context, argv []string) *exec.Cmd {
	args := make([]string, len(argv))
	for i, arg := range argv {
		args[i] = r.expand(arg)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = r.options.Reconcile.RootPath
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "DOTPKG_BIN="+r.executable, "DOTPKG_SELECTIONS_FILE="+r.selectionsPath(), "DOTPKG_YES=1", "DOTPKG_WIZARD=1", "INSTALL_PROFILE="+r.session.Profile, "INSTALL_HOST="+r.session.Host)
	for key, value := range r.session.Inputs {
		cmd.Env = append(cmd.Env, "DOTPKG_INPUT_"+strings.ToUpper(strings.ReplaceAll(key, "-", "_"))+"="+value)
	}
	if r.options.Plain {
		cmd.Env = append(cmd.Env, "DOTPKG_NONINTERACTIVE=1")
	}
	if r.options.PackagesOnly {
		cmd.Env = append(cmd.Env, "DOTPKG_PACKAGES_ONLY=1")
	}
	if r.options.Reconcile.RestartServices {
		cmd.Env = append(cmd.Env, "DOTPKG_RESTART_SERVICES=1")
	}
	return cmd
}

func (r *runtime) inspect(argv []string) (string, error) {
	if len(argv) == 0 {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var buffer limitedBuffer
	cmd := r.command(ctx, argv)
	cmd.Stdout = &buffer
	cmd.Stderr = io.Discard
	err := cmd.Run()
	return strings.TrimSpace(buffer.String()), err
}

// Checks and public display commands cannot fill memory or escape the TUI.
type limitedBuffer struct{ data []byte }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(b.data) < 32768 {
		b.data = append(b.data, p[:min(len(p), 32768-len(b.data))]...)
	}
	return n, nil
}
func (b *limitedBuffer) String() string { return clean(string(b.data)) }
func clean(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (r >= 32 && r != 127 && !(r >= 128 && r <= 159)) {
			return r
		}
		return -1
	}, text)
}

func (r *runtime) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	text := clean(string(p))
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > 0 {
		r.activity = lines[len(lines)-1]
	}
	if r.log != nil {
		_, _ = r.log.WriteString(text)
	}
	return len(p), nil
}

func (r *runtime) event(step, status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.events != nil {
		_ = json.NewEncoder(r.events).Encode(map[string]any{"schema": "dotpkg.wizard.event", "version": 1, "time": time.Now().UTC(), "step": step, "status": status})
	}
	if r.log != nil {
		_, _ = fmt.Fprintf(r.log, "%s %s: %s\n", time.Now().Format(time.RFC3339), step, status)
	}
}

func (r *runtime) begin() error {
	if err := r.save(); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(r.directory, "run.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	r.log = f
	r.events, err = os.OpenFile(filepath.Join(r.directory, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	return r.continuation(true)
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

// A handoff runs with SIGINT ignored so cancelling an installer does not kill
// a package transaction halfway through. Tools may still implement their own
// cancellation. Account steps are never recorded unless explicitly opted in.
func (r *runtime) handoff(step Step) (*exec.Cmd, error) {
	cmd := r.command(context.Background(), step.Action)
	quoted := make([]string, len(cmd.Args))
	for i, arg := range cmd.Args {
		quoted[i] = shellQuote(arg)
	}
	command := "trap '' INT; printf '%s\\n' " + shellQuote(step.Title+" — terminal handoff. Finish this operation to return to the wizard.") + "; " + strings.Join(quoted, " ")
	var wrapper *exec.Cmd
	if step.LogOutput {
		if script, err := exec.LookPath("script"); err == nil {
			path := filepath.Join(r.directory, step.ID+".log")
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				return nil, err
			}
			if err := file.Close(); err != nil {
				return nil, err
			}
			wrapper = exec.Command(script, "--quiet", "--return", "--flush", "--append", "--log-out", path, "--command", command)
		}
	}
	if wrapper == nil {
		wrapper = exec.Command("bash", "-c", command)
	}
	wrapper.Dir = cmd.Dir
	wrapper.Env = cmd.Env
	return wrapper, nil
}

func desktopQuote(value string) string {
	value = strings.ReplaceAll(value, "%", "%%")
	value = strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "`", "\\`", "$", "\\$").Replace(value)
	return "\"" + value + "\""
}

func (r *runtime) continuation(enable bool) error {
	if !r.spec.ContinueOnLogin || r.session.Profile != "desktop" || r.options.PackagesOnly {
		return nil
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		home, _ := os.UserHomeDir()
		config = filepath.Join(home, ".config")
	}
	name := fmt.Sprintf("dotpkg-onboarding-%x.desktop", sha256.Sum256([]byte(r.options.Reconcile.ManifestPath)))
	path := filepath.Join(config, "autostart", name)
	if !enable {
		if err := os.Remove(filepath.Join(r.directory, "resume.json")); err != nil && !os.IsNotExist(err) {
			return err
		}
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	args := []string{r.executable, "wizard", "--resume", "--manifest", r.options.Reconcile.ManifestPath, "--root", r.options.Reconcile.RootPath, "--state-file", r.options.Reconcile.StatePath}
	if err := atomicJSON(filepath.Join(r.directory, "resume.json"), map[string]any{"root": r.options.Reconcile.RootPath, "args": args}); err != nil {
		return err
	}
	for i := range args {
		args[i] = desktopQuote(args[i])
	}
	contents := "[Desktop Entry]\nType=Application\nName=Finish machine setup\nComment=Continue pending dotpkg onboarding\nTerminal=true\nExec=" + strings.Join(args, " ") + "\n"
	if strings.ContainsAny(r.executable+r.options.Reconcile.ManifestPath+r.options.Reconcile.RootPath+r.options.Reconcile.StatePath, "\n\r") {
		return fmt.Errorf("cannot register continuation with newline in path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".dotpkg-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(contents); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
