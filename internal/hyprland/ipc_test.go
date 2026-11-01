package hyprland

import (
	"errors"
	"os"
	"strings"
	"testing"
)

type fakeHyprRunner struct {
	outputs map[string]string
}

func (f *fakeHyprRunner) LookPath(file string) (string, error) {
	return "/usr/bin/" + file, nil
}

func (f *fakeHyprRunner) Run(name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	if out, ok := f.outputs[key]; ok {
		return []byte(out), nil
	}
	return nil, errors.New("unexpected command: " + key)
}

func TestParseWorkspaceEvent(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantOK  bool
		wantErr bool
		want    WorkspaceEvent
	}{
		{
			name:   "workspacev2 numeric",
			line:   "workspacev2>>3,3",
			wantOK: true,
			want:   WorkspaceEvent{Type: "workspacev2", ID: 3, Name: "3", HasID: true},
		},
		{
			name:   "workspacev2 named with comma",
			line:   "workspacev2>>7,code,review\n",
			wantOK: true,
			want:   WorkspaceEvent{Type: "workspacev2", ID: 7, Name: "code,review", HasID: true},
		},
		{
			name:   "workspace legacy numeric",
			line:   "workspace>>4",
			wantOK: true,
			want:   WorkspaceEvent{Type: "workspace", ID: 4, Name: "4", HasID: true},
		},
		{
			name:   "workspace legacy custom name",
			line:   "workspace>>browser",
			wantOK: true,
			want:   WorkspaceEvent{Type: "workspace", ID: 0, Name: "browser", HasID: false},
		},
		{
			name:   "special workspacev2 ignored",
			line:   "workspacev2>>-99,special:scratch",
			wantOK: false,
		},
		{
			name:   "special workspace ignored",
			line:   "workspace>>special:magic",
			wantOK: false,
		},
		{
			name:   "unrelated event ignored",
			line:   "activewindow>>kitty,zsh",
			wantOK: false,
		},
		{
			name:    "malformed workspacev2 ID",
			line:    "workspacev2>>notanid,main",
			wantOK:  false,
			wantErr: true,
		},
		{
			name:    "empty workspace payload",
			line:    "workspace>>",
			wantOK:  false,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := ParseWorkspaceEvent(tc.line)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tc.line)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.line, err)
			}
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestResolveSocket2Path(t *testing.T) {
	if _, err := ResolveSocket2Path("/run/user/1000", "", nil); !errors.Is(err, ErrNotInHyprland) {
		t.Fatalf("expected ErrNotInHyprland, got %v", err)
	}

	existing := map[string]bool{
		"/run/user/1000/hypr/sig123/.socket2.sock": true,
	}
	statFn := func(p string) (os.FileInfo, error) {
		if existing[p] {
			return nil, nil
		}
		return nil, os.ErrNotExist
	}

	got, err := ResolveSocket2Path("/run/user/1000", "sig123", statFn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/run/user/1000/hypr/sig123/.socket2.sock" {
		t.Errorf("got %q", got)
	}

	// Fallback to /tmp/hypr when XDG_RUNTIME_DIR path does not exist.
	existing = map[string]bool{
		"/tmp/hypr/sig123/.socket2.sock": true,
	}
	got, err = ResolveSocket2Path("/run/user/1000", "sig123", statFn)
	if err != nil {
		t.Fatalf("unexpected fallback error: %v", err)
	}
	if got != "/tmp/hypr/sig123/.socket2.sock" {
		t.Errorf("got %q, want /tmp fallback", got)
	}
}

func TestClientWorkspacesSortingAndFiltering(t *testing.T) {
	runner := &fakeHyprRunner{
		outputs: map[string]string{
			"hyprctl workspaces -j": `[
				{"id": 4, "name": "4", "monitor": "eDP-1", "windows": 1},
				{"id": -99, "name": "special:scratch", "monitor": "eDP-1", "windows": 1},
				{"id": 1, "name": "1", "monitor": "eDP-1", "windows": 2},
				{"id": 2, "name": "2", "monitor": "eDP-1", "windows": 1}
			]`,
			"hyprctl activeworkspace -j": `{"id": 2, "name": "2", "monitor": "eDP-1", "windows": 1}`,
		},
	}

	client := NewClientWithRunner(runner)
	list, err := client.Workspaces()
	if err != nil {
		t.Fatalf("Workspaces() failed: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len(list) = %d, want 3 (special filtered out)", len(list))
	}
	if list[0].ID != 1 || list[1].ID != 2 || list[2].ID != 4 {
		t.Errorf("unexpected order: %+v", list)
	}

	active, err := client.ActiveWorkspace()
	if err != nil {
		t.Fatalf("ActiveWorkspace() failed: %v", err)
	}
	if active.ID != 2 {
		t.Errorf("active.ID = %d, want 2", active.ID)
	}
}
