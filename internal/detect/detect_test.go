package detect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProfile(t *testing.T) {
	tests := []struct {
		name    string
		files   []string
		want    string
	}{
		{
			name:  "go project",
			files: []string{"go.mod"},
			want:  "go",
		},
		{
			name:  "rust project",
			files: []string{"Cargo.toml"},
			want:  "rust",
		},
		{
			name:  "python pyproject",
			files: []string{"pyproject.toml"},
			want:  "python",
		},
		{
			name:  "python setup.py",
			files: []string{"setup.py"},
			want:  "python",
		},
		{
			name:  "python requirements",
			files: []string{"requirements.txt"},
			want:  "python",
		},
		{
			name:  "python pipfile",
			files: []string{"Pipfile"},
			want:  "python",
		},
		{
			name:  "web project",
			files: []string{"package.json"},
			want:  "web",
		},
		{
			name:  "k8s helmfile",
			files: []string{"helmfile.yaml"},
			want:  "k8s",
		},
		{
			name:  "k8s kustomization",
			files: []string{"kustomization.yaml"},
			want:  "k8s",
		},
		{
			name:  "k8s chart",
			files: []string{"Chart.yaml"},
			want:  "k8s",
		},
		{
			name:  "devops dockerfile",
			files: []string{"Dockerfile"},
			want:  "devops",
		},
		{
			name:  "empty directory",
			files: nil,
			want:  "",
		},
		{
			name:  "unrecognized files",
			files: []string{"README.md", "Makefile"},
			want:  "",
		},
		{
			name:  "rust wins over go by priority",
			files: []string{"Cargo.toml", "go.mod"},
			want:  "rust",
		},
		{
			name:  "go wins over python by priority",
			files: []string{"go.mod", "requirements.txt"},
			want:  "go",
		},
		{
			name:  "web wins over devops by priority",
			files: []string{"package.json", "Dockerfile"},
			want:  "web",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tt.files {
				path := filepath.Join(dir, f)
				if err := os.WriteFile(path, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := Profile(dir)
			if got != tt.want {
				t.Errorf("Profile() = %q, want %q", got, tt.want)
			}
		})
	}
}
