package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type fakeExitError struct{ code int }

func (e fakeExitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e fakeExitError) ExitCode() int { return e.code }

type runnerCall struct {
	kind      string
	name      string
	args      []string
	directory string
}

type fakeRunner struct {
	lookPath map[string]error
	outputs  map[string]fakeOutput
	calls    []runnerCall
}

type fakeOutput struct {
	value []byte
	err   error
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	f.calls = append(f.calls, runnerCall{kind: "lookpath", name: name})
	if err, ok := f.lookPath[name]; ok {
		return "", err
	}
	return "/usr/bin/" + name, nil
}

func (f *fakeRunner) Output(_ context.Context, name string, args []string) ([]byte, error) {
	f.calls = append(f.calls, runnerCall{kind: "output", name: name, args: append([]string{}, args...)})
	if output, ok := f.outputs[name]; ok {
		return output.value, output.err
	}
	return nil, nil
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, directory string) error {
	f.calls = append(f.calls, runnerCall{kind: "run", name: name, args: append([]string{}, args...), directory: directory})
	return nil
}

func TestArchIsInstalled(t *testing.T) {
	for _, test := range []struct {
		name      string
		outputErr error
		want      bool
		wantErr   bool
	}{
		{name: "installed", want: true},
		{name: "not installed", outputErr: fakeExitError{code: 1}},
		{name: "command failure", outputErr: fakeExitError{code: 2}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{outputs: map[string]fakeOutput{
				"pacman": {err: test.outputErr},
			}}
			arch := &Arch{Runner: runner}
			got, err := arch.IsInstalled(context.Background(), "git")
			if got != test.want {
				t.Fatalf("installed = %v, want %v", got, test.want)
			}
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, want error: %v", err, test.wantErr)
			}
		})
	}
}

func TestArchRepositoryPackages(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]fakeOutput{
		"pacman": {value: []byte("git\nbat\ngit\n\n")},
	}}
	arch := &Arch{Runner: runner}

	got, err := arch.RepositoryPackages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{}{"git": {}, "bat": {}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("packages = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(runner.calls[1].args, []string{"-Slq"}) {
		t.Fatalf("pacman args = %#v", runner.calls[1].args)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestArchAURPackagesBatchesRequests(t *testing.T) {
	names := make([]string, 201)
	for index := range names {
		names[index] = fmt.Sprintf("package-%03d", index)
	}
	var requests []*http.Request
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request)
		values := request.URL.Query()["arg[]"]
		results := make([]string, len(values))
		for index, value := range values {
			results[index] = `{"Name":"` + value + `"}`
		}
		body := `{"results":[` + strings.Join(results, ",") + `]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
	arch := &Arch{HTTPClient: client}

	got, err := arch.AURPackages(context.Background(), names)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(names) {
		t.Fatalf("returned %d packages, want %d", len(got), len(names))
	}
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	if got := len(requests[0].URL.Query()["arg[]"]); got != 200 {
		t.Fatalf("first batch size = %d, want 200", got)
	}
	if got := len(requests[1].URL.Query()["arg[]"]); got != 1 {
		t.Fatalf("second batch size = %d, want 1", got)
	}
}

func TestArchAURPackagesRetriesTransientFailures(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		if attempts < 2 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Status:     "503 Service Unavailable",
				Body:       io.NopCloser(strings.NewReader("busy")),
				Header:     make(http.Header),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{"results":[{"Name":"git"}]}`)),
			Header:     make(http.Header),
		}, nil
	})}
	arch := &Arch{HTTPClient: client}
	got, err := arch.AURPackages(context.Background(), []string{"git"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["git"]; !ok || attempts != 2 {
		t.Fatalf("packages = %#v, attempts = %d", got, attempts)
	}
}

func TestArchAURPackagesReportsHTTPError(t *testing.T) {
	arch := &Arch{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Status:     "502 Bad Gateway",
			Body:       io.NopCloser(strings.NewReader("not json")),
			Header:     make(http.Header),
		}, nil
	})}}

	_, err := arch.AURPackages(context.Background(), []string{"missing"})
	if err == nil || !strings.Contains(err.Error(), "AUR returned HTTP 502 Bad Gateway") {
		t.Fatalf("error = %v", err)
	}
}

func TestArchAURPackagesReportsTransportError(t *testing.T) {
	want := errors.New("network unavailable")
	arch := &Arch{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, want
	})}}

	_, err := arch.AURPackages(context.Background(), []string{"missing"})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped transport error", err)
	}
}

func TestArchInstallCommands(t *testing.T) {
	runner := &fakeRunner{}
	arch := &Arch{Runner: runner}

	if err := arch.Install(context.Background(), []string{"git", "pkg;echo unsafe"}, []string{"google-chrome"}, "desktop"); err != nil {
		t.Fatal(err)
	}
	var runs []runnerCall
	for _, call := range runner.calls {
		if call.kind == "run" {
			runs = append(runs, call)
		}
	}
	if len(runs) != 2 {
		t.Fatalf("run calls = %#v", runs)
	}
	if !reflect.DeepEqual(runs[0].args, []string{"pacman", "-S", "--needed", "--noconfirm", "git", "pkg;echo unsafe"}) {
		t.Fatalf("repository args = %#v", runs[0].args)
	}
	if !reflect.DeepEqual(runs[1].args, []string{"-S", "--needed", "--noconfirm", "google-chrome"}) {
		t.Fatalf("AUR args = %#v", runs[1].args)
	}
}

func TestArchInstallBootstrapsYay(t *testing.T) {
	runner := &fakeRunner{lookPath: map[string]error{"yay": errors.New("not found")}}
	arch := &Arch{Runner: runner}

	if err := arch.Install(context.Background(), nil, []string{"google-chrome"}, "desktop"); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, call := range runner.calls {
		if call.kind == "run" {
			names = append(names, call.name)
		}
	}
	if !reflect.DeepEqual(names, []string{"git", "git", "makepkg", "yay"}) {
		t.Fatalf("run commands = %#v", names)
	}
	for _, call := range runner.calls {
		if call.kind == "run" && call.name == "makepkg" && call.directory == "" {
			t.Fatal("makepkg was not run in the checkout directory")
		}
	}
}

func TestArchRemoveUsesProfileBackend(t *testing.T) {
	for _, test := range []struct {
		profile string
		command string
		prefix  []string
	}{
		{profile: "server", command: "sudo", prefix: []string{"pacman", "-Rns", "--noconfirm"}},
		{profile: "desktop", command: "yay", prefix: []string{"-Rns", "--noconfirm"}},
	} {
		t.Run(test.profile, func(t *testing.T) {
			runner := &fakeRunner{}
			arch := &Arch{Runner: runner}
			if err := arch.Remove(context.Background(), []string{"old-package"}, test.profile); err != nil {
				t.Fatal(err)
			}
			if len(runner.calls) == 0 || runner.calls[len(runner.calls)-1].name != test.command {
				t.Fatalf("calls = %#v", runner.calls)
			}
			call := runner.calls[len(runner.calls)-1]
			want := append(test.prefix, "old-package")
			if !reflect.DeepEqual(call.args, want) {
				t.Fatalf("args = %#v, want %#v", call.args, want)
			}
		})
	}
}

func TestLinesToSetTrimsAndSortsForReadableFailure(t *testing.T) {
	set := linesToSet(" zsh\n\n bash \n")
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	if !reflect.DeepEqual(values, []string{"bash", "zsh"}) {
		t.Fatalf("values = %#v", values)
	}
}

func TestAURQueryEscapesPackageNames(t *testing.T) {
	var query url.Values
	arch := &Arch{HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		query = request.URL.Query()
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{"results":[]}`)),
			Header:     make(http.Header),
		}, nil
	})}}

	if _, err := arch.AURPackages(context.Background(), []string{"name with spaces", "name&value"}); err != nil {
		t.Fatal(err)
	}
	if got := query.Get("arg[]"); got != "name with spaces" {
		t.Fatalf("first query value = %q", got)
	}
	if got := query["arg[]"][1]; got != "name&value" {
		t.Fatalf("second query value = %q", got)
	}
}

func TestBootstrapYayUsesTemporaryCheckout(t *testing.T) {
	runner := &fakeRunner{}
	arch := &Arch{Runner: runner}
	if err := arch.bootstrapYay(context.Background()); err != nil {
		t.Fatal(err)
	}
	var cloned bool
	for _, call := range runner.calls {
		if call.kind == "run" && call.name == "git" {
			if len(call.args) != 3 || call.args[0] != "clone" || filepath.Base(call.args[2]) != "yay-git" {
				continue
			}
			cloned = true
		}
	}
	if !cloned {
		t.Fatal("git clone was not called")
	}
	var pinned bool
	for _, call := range runner.calls {
		if call.kind == "run" && call.name == "git" && len(call.args) == 5 &&
			call.args[0] == "-C" && call.args[2] == "checkout" && call.args[3] == "--detach" &&
			call.args[4] == yayBootstrapRevision {
			pinned = true
		}
	}
	if !pinned {
		t.Fatal("yay checkout was not pinned")
	}
}

func TestArchResourceCommands(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string]fakeOutput{
			"id":        {value: []byte("wheel docker\n")},
			"getent":    {value: []byte("docker:x:991:user\n")},
			"systemctl": {value: []byte("enabled\n")},
		},
	}
	arch := &Arch{Runner: runner}

	groups, err := arch.CurrentGroups(context.Background(), "lukas")
	if err != nil || !reflect.DeepEqual(groups, []string{"wheel", "docker"}) {
		t.Fatalf("groups = %#v, error = %v", groups, err)
	}
	if exists, err := arch.GroupExists(context.Background(), "docker"); err != nil || !exists {
		t.Fatalf("group exists = %v, error = %v", exists, err)
	}
	if exists, err := arch.ServiceExists(context.Background(), true, "example.service"); err != nil || !exists {
		t.Fatalf("service exists = %v, error = %v", exists, err)
	}
	if enabled, err := arch.ServiceEnabled(context.Background(), true, "example.service"); err != nil || !enabled {
		t.Fatalf("service enabled = %v, error = %v", enabled, err)
	}
	if err := arch.AddToGroup(context.Background(), "lukas", "video"); err != nil {
		t.Fatal(err)
	}
	if err := arch.RemoveFromGroup(context.Background(), "lukas", "docker"); err != nil {
		t.Fatal(err)
	}
	if err := arch.EnableService(context.Background(), true, "example.service"); err != nil {
		t.Fatal(err)
	}
	if err := arch.ReloadServices(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := arch.RestartService(context.Background(), true, "example.service"); err != nil {
		t.Fatal(err)
	}
	if err := arch.DisableService(context.Background(), false, "docker.service"); err != nil {
		t.Fatal(err)
	}

	var runs []runnerCall
	for _, call := range runner.calls {
		if call.kind == "run" {
			runs = append(runs, call)
		}
	}
	want := [][]string{
		{"usermod", "-aG", "video", "lukas"},
		{"gpasswd", "-d", "lukas", "docker"},
		{"--user", "enable", "--now", "example.service"},
		{"systemctl", "daemon-reload"},
		{"--user", "try-restart", "example.service"},
		{"systemctl", "disable", "--now", "docker.service"},
	}
	if len(runs) != len(want) {
		t.Fatalf("run calls = %#v", runs)
	}
	for index, call := range runs {
		if !reflect.DeepEqual(call.args, want[index]) {
			t.Fatalf("run %d args = %#v, want %#v", index, call.args, want[index])
		}
	}
}
