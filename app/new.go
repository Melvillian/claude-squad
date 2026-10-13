package app

import (
	"claude-squad/config"
	"claude-squad/session"
	"context"
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

// maxTitleWidth is the longest session title the TUI accepts.
const maxTitleWidth = 32

// NewOptions holds the answers `cs new` takes from flags in place of the
// interactive new-session wizard.
type NewOptions struct {
	// Path is the repository directory. Empty means the current directory.
	// Exclusive with Template.
	Path string
	// Template is a repo template to create the repository from: either the
	// name of an entry in config.RepoTemplates or an "owner/repo". Requires RepoName.
	Template string
	// RepoName is the name of the GitHub repo created from Template.
	RepoName string
	// Title is the session title. Defaults to RepoName when a template is used.
	Title string
	// Prompt is typed into the program once it has started.
	Prompt string
	// Branch is an existing branch to start from. Empty creates a new branch from HEAD.
	Branch string
	// Program overrides the configured program.
	Program string
	// RemoteControl is "always", "never" or empty to follow the config. The
	// config's "ask" mode resolves to never since there is nobody to ask.
	RemoteControl string
}

// RunNew creates and starts a session from opts without the TUI, so sessions
// can be opened from scripts. A running TUI notices the new record in the
// state file (see refreshInstances) and adds the session to its list.
func RunNew(ctx context.Context, opts NewOptions) error {
	cfg := config.LoadConfig()
	storage, err := session.NewStorage(config.LoadState())
	if err != nil {
		return fmt.Errorf("failed to initialize storage: %w", err)
	}

	instance, err := newInstanceFromOptions(ctx, cfg, storage, opts)
	if err != nil {
		return err
	}
	if err := startInstanceHeadless(instance, storage); err != nil {
		return err
	}

	worktree, err := instance.GetGitWorktree()
	if err != nil {
		return err
	}
	fmt.Printf("Started session %q\n  branch:   %s\n  worktree: %s\n  attach:   tmux attach -t %s\n",
		instance.Title, instance.Branch, worktree.GetWorktreePath(), instance.TmuxSessionName())
	return nil
}

// newInstanceFromOptions validates opts, creates the repository from a template
// if requested, and returns the unstarted instance the wizard would have built.
func newInstanceFromOptions(ctx context.Context, cfg *config.Config, storage *session.Storage, opts NewOptions) (*session.Instance, error) {
	program := opts.Program
	if program == "" {
		program = cfg.GetProgram()
	}
	remoteControl, err := resolveRemoteControl(cfg, opts.RemoteControl, program)
	if err != nil {
		return nil, err
	}

	switch {
	case opts.Template != "" && opts.RepoName == "":
		return nil, fmt.Errorf("--template requires --repo-name")
	case opts.Template == "" && opts.RepoName != "":
		return nil, fmt.Errorf("--repo-name requires --template")
	case opts.Template != "" && opts.Path != "":
		return nil, fmt.Errorf("--template and --path are mutually exclusive")
	}

	title := strings.TrimSpace(opts.Title)
	if title == "" && opts.Template != "" {
		title = opts.RepoName
	}
	if title == "" {
		return nil, fmt.Errorf("title cannot be empty")
	}
	if runewidth.StringWidth(title) > maxTitleWidth {
		return nil, fmt.Errorf("title cannot be longer than %d characters", maxTitleWidth)
	}
	exists, err := storage.TitleExists(title)
	if err != nil {
		return nil, fmt.Errorf("failed to read stored sessions: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("a session titled %q already exists", title)
	}

	var path string
	if opts.Template != "" {
		template, err := resolveTemplate(cfg, opts.Template)
		if err != nil {
			return nil, err
		}
		path, err = createRepoFromTemplate(ctx, cfg.GetRepoTemplatesDir(), opts.RepoName, template)
		if err != nil {
			return nil, err
		}
	} else {
		input := opts.Path
		if input == "" {
			input = "."
		}
		path, err = validateRepoDir(input)
		if err != nil {
			return nil, err
		}
	}

	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   title,
		Path:    path,
		Program: program,
		Branch:  opts.Branch,
	})
	if err != nil {
		return nil, err
	}
	if opts.Branch != "" {
		instance.SetSelectedBranch(opts.Branch)
	}
	instance.Prompt = opts.Prompt
	instance.RemoteControl = remoteControl
	return instance, nil
}

// startInstanceHeadless starts the instance, persists it, and types its
// startup messages, mirroring what the TUI does across instanceStartedMsg.
func startInstanceHeadless(instance *session.Instance, storage *session.Storage) error {
	if err := startInstanceAndSave(instance, storage); err != nil {
		return err
	}
	remoteControl, prompt := takeStartupMessages(instance)
	return sendStartupMessages(instance, remoteControl, prompt)
}

// resolveTemplate maps a configured template name to its "owner/repo". A value
// that already looks like "owner/repo" is returned unchanged.
func resolveTemplate(cfg *config.Config, template string) (string, error) {
	names := make([]string, 0, len(cfg.RepoTemplates))
	for _, t := range cfg.RepoTemplates {
		if t.Name == template {
			return t.Repo, nil
		}
		names = append(names, t.Name)
	}
	if strings.Contains(template, "/") {
		return template, nil
	}
	if len(names) == 0 {
		return "", fmt.Errorf("unknown template %q: no repo_templates are configured; pass an owner/repo instead", template)
	}
	return "", fmt.Errorf("unknown template %q: configured templates are %s; or pass an owner/repo",
		template, strings.Join(names, ", "))
}

// resolveRemoteControl decides whether the new instance starts with
// remoteControlCommand. flag ("always"/"never") overrides the config; the
// config's "ask" mode means never, since nobody can answer non-interactively.
// Non-claude programs never get it, as in the TUI.
func resolveRemoteControl(cfg *config.Config, flag string, program string) (bool, error) {
	mode := cfg.GetRemoteControl()
	switch config.RemoteControlMode(flag) {
	case "":
	case config.RemoteControlAlways, config.RemoteControlNever:
		mode = config.RemoteControlMode(flag)
	default:
		return false, fmt.Errorf("invalid --remote-control %q: use %q or %q", flag,
			config.RemoteControlAlways, config.RemoteControlNever)
	}
	if mode != config.RemoteControlAlways {
		return false, nil
	}
	probe := &session.Instance{Program: program}
	return probe.IsClaude(), nil
}
