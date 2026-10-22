package skill

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	systemfs "github.com/luank3/gax/internal/system/fs"
	terminalgit "github.com/luank3/gax/internal/system/git"
	systempath "github.com/luank3/gax/internal/system/path"
)

const (
	// frontmatterCap bounds the read used to find a skill's description.
	frontmatterCap = 8 * 1024
	// descriptionCap bounds the listed description.
	descriptionCap = 1024
	// documentCap bounds a loaded document; a skill larger than this is a mistake, not a document.
	documentCap = 1 << 20
	// projectWalkMaxDepth bounds the walk from the working directory to the git worktree root.
	projectWalkMaxDepth = 64
)

// ErrDocumentTooLarge reports a SKILL.md past documentCap.
var ErrDocumentTooLarge = errors.New("skill document is too large to load")

// Entry is one skill a session can load: a discovered SKILL.md, or a document embedded in the
// binary.
type Entry struct {
	Name string
	// Path is the real location: a filesystem path, or the gax: path of a built-in.
	Path string
	// DisplayPath collapses the home directory for listings.
	DisplayPath string
	Description string
	// Body carries an embedded document; a discovered skill is read from Path when it is loaded.
	Body string
}

// Options locates the roots a session discovers skills from.
type Options struct {
	Home string
	CWD  string
}

// Discover returns the skills a session can load, nearest-first: project roots from the working
// directory up to the git worktree root, the gax config directory, the shared ~/.agents/skills
// root, and finally the embedded documents, which any discovered skill of the same name shadows.
// The result is sorted by name.
func Discover(options Options) []Entry {
	found := make(map[string]Entry)
	sharedRoot := ""
	var sharedRootInfo os.FileInfo
	if options.Home != "" {
		sharedRoot = systempath.Join(options.Home, ".agents/skills")
		sharedRootInfo, _ = os.Stat(sharedRoot)
	}
	if options.CWD != "" {
		root := terminalgit.FindWorktreeRoot(options.CWD)
		for dir, depth := options.CWD, 0; depth < projectWalkMaxDepth; depth++ {
			projectSkills := systempath.Join(dir, ".agents/skills")
			sharedAlias := false
			if sharedRootInfo != nil {
				if projectInfo, err := os.Stat(projectSkills); err == nil && os.SameFile(projectInfo, sharedRootInfo) {
					sharedAlias = true
				}
			}
			if !sharedAlias {
				collect(projectSkills, options.Home, found)
			}
			if root == "" || filepath.Clean(root) == filepath.Clean(dir) {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if directory, ok := configSkillsPath(); ok {
		collect(directory, options.Home, found)
	}
	if sharedRoot != "" {
		collect(sharedRoot, options.Home, found)
	}
	// Embedded skills are the fallback, so a discovered skill with the same name shadows them.
	for _, builtin := range Builtin() {
		if _, exists := found[builtin.Name]; exists {
			continue
		}
		found[builtin.Name] = Entry{
			Name: builtin.Name, Path: builtin.Path, DisplayPath: builtin.Path,
			Description: description([]byte(builtin.Body)), Body: builtin.Body,
		}
	}
	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]Entry, 0, len(names))
	for _, name := range names {
		entries = append(entries, found[name])
	}
	return entries
}

// Load returns the skill's document.
func (entry Entry) Load() (string, error) {
	if entry.Body != "" {
		return entry.Body, nil
	}
	data, truncated, err := systemfs.ReadFileCapped(entry.Path, documentCap)
	if err != nil {
		return "", err
	}
	if truncated {
		return "", ErrDocumentTooLarge
	}
	return string(data), nil
}

func collect(root, home string, found map[string]Entry) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		name := cleanText(entry.Name())
		if _, exists := found[name]; exists {
			continue
		}
		path := systempath.Join(systempath.Join(root, entry.Name()), "SKILL.md")
		data, _, err := systemfs.ReadFileCapped(path, frontmatterCap)
		if err != nil {
			continue
		}
		found[name] = Entry{
			Name: name, Path: path, DisplayPath: cleanText(systempath.CollapseHome(path, home)),
			Description: description(data),
		}
	}
}

// configSkillsPath resolves ~/.config/gax/skills.
func configSkillsPath() (string, bool) {
	return systempath.XDGGaxPath(os.Getenv("XDG_CONFIG_HOME"), systempath.Home(), ".config", "skills")
}

func description(data []byte) string {
	return truncateUTF8(cleanText(frontmatterScalarLine(data, "description")), descriptionCap)
}

func cleanText(value string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(value, "\x00", "\uFFFD"), "\uFFFD")
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
