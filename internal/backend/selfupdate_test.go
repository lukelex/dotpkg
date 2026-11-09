package backend

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type updateRunner struct {
	outputs map[string]string
	calls   []string
	err     map[string]error
}

func (r *updateRunner) LookPath(name string) (string, error) {
	r.calls = append(r.calls, "look "+name)
	if err := r.err["look "+name]; err != nil {
		return "", err
	}
	return "/usr/bin/" + name, nil
}
func (r *updateRunner) Output(_ context.Context, name string, args []string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, key)
	return []byte(r.outputs[key]), r.err[key]
}
func (r *updateRunner) Run(_ context.Context, name string, args []string, _ string) error {
	key := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, key)
	return r.err[key]
}

func TestSelfUpdateOwnership(t *testing.T) {
	ctx := context.Background()
	for _, owner := range []string{"dotpkg\n", "other\n"} {
		r := &updateRunner{outputs: map[string]string{"pacman -Qoq -- /usr/bin/dotpkg": owner}}
		err := (&Arch{Runner: r}).VerifyExecutable(ctx, "/usr/bin/dotpkg", "dotpkg")
		if (err != nil) != (owner != "dotpkg\n") {
			t.Fatalf("owner %q: %v", owner, err)
		}
	}
	r := &updateRunner{err: map[string]error{"pacman -Qoq -- /usr/local/bin/dotpkg": errors.New("not owned")}}
	if err := (&Arch{Runner: r}).VerifyExecutable(ctx, "/usr/local/bin/dotpkg", "dotpkg"); err == nil || !strings.Contains(err.Error(), "standalone") {
		t.Fatalf("standalone: %v", err)
	}
}

func TestSelfUpdateVersionsAndCommands(t *testing.T) {
	for _, origin := range []string{"repo", "aur"} {
		for _, dry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dry=%v", origin, dry), func(t *testing.T) {
				manager, infoArgs := "pacman", "-Si dotpkg"
				if origin == "aur" {
					manager, infoArgs = "yay", "--aur -Si dotpkg"
				}
				r := &updateRunner{outputs: map[string]string{
					"pacman -Q dotpkg":       "dotpkg 1.0-1\n",
					manager + " " + infoArgs: "Name : dotpkg\nVersion : 1.1-1\n",
					"vercmp 1.1-1 1.0-1":     "1\n",
				}}
				a := &Arch{Runner: r}
				old, next, newer, err := a.AvailableUpdate(context.Background(), "dotpkg", origin, dry)
				if err != nil || !newer || old != "1.0-1" || next != "1.1-1" {
					t.Fatalf("version = %q %q %v, err %v", old, next, newer, err)
				}
				refreshed := false
				for _, call := range r.calls {
					if call == "sudo pacman -Sy --noconfirm" {
						refreshed = true
					}
				}
				if refreshed != (origin == "repo" && !dry) {
					t.Fatalf("unexpected refresh: %v", r.calls)
				}
				if err := a.UpgradePackage(context.Background(), "dotpkg", origin); err != nil {
					t.Fatal(err)
				}
				want := "sudo pacman -S --needed --noconfirm dotpkg"
				if origin == "aur" {
					want = "yay --aur -S --needed --noconfirm dotpkg"
				}
				if got := r.calls[len(r.calls)-1]; got != want {
					t.Fatalf("upgrade = %q, want %q", got, want)
				}
			})
		}
	}
}

func TestSelfUpdateNoReinstallAndFailures(t *testing.T) {
	r := &updateRunner{outputs: map[string]string{"pacman -Q dotpkg": "dotpkg 2\n", "pacman -Si dotpkg": "Version : 1\n", "vercmp 1 2": "-1\n"}}
	old, next, newer, err := (&Arch{Runner: r}).AvailableUpdate(context.Background(), "dotpkg", "repo", true)
	if err != nil || newer || !reflect.DeepEqual([]string{old, next}, []string{"2", "1"}) {
		t.Fatalf("old=%q next=%q newer=%v err=%v", old, next, newer, err)
	}
	r.err = map[string]error{"look yay": errors.New("missing")}
	if _, _, _, err := (&Arch{Runner: r}).AvailableUpdate(context.Background(), "dotpkg", "aur", false); err == nil {
		t.Fatal("missing yay accepted")
	}
	r.err = map[string]error{"pacman -Qn dotpkg": errors.New("foreign package")}
	if _, _, _, err := (&Arch{Runner: r}).AvailableUpdate(context.Background(), "dotpkg", "repo", false); err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("mismatched origin accepted: %v", err)
	}
}
