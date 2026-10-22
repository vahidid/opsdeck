package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"opsdeck/internal/workspace"
)

// Volume is a docker volume with the containers that mount it.
type Volume struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Scope      string            `json:"scope"`
	Mountpoint string            `json:"mountpoint"`
	Labels     map[string]string `json:"labels,omitempty"`
	Project    string            `json:"project,omitempty"` // compose project
	Stack      string            `json:"stack,omitempty"`
	Containers []string          `json:"containers"`
	Anonymous  bool              `json:"anonymous,omitempty"`
}

func (c *Client) Volumes(ctx context.Context, w *workspace.Workspace, host *workspace.Resource) ([]Volume, error) {
	out, err := c.runOK(ctx, w, host, "volume ls --format '{{json .}}'")
	if err != nil {
		return nil, err
	}
	raw, err := jsonLines[map[string]any](out)
	if err != nil {
		return nil, err
	}
	users := map[string][]string{}
	if cts, err := c.Containers(ctx, w, host, true); err == nil {
		for _, ct := range cts {
			for _, m := range strings.Split(ct.Mounts, ",") {
				if m = strings.TrimSpace(m); m != "" {
					users[m] = append(users[m], ct.Name)
				}
			}
		}
	}
	list := []Volume{}
	for _, r := range raw {
		l := labels(str(r["Labels"]))
		if str(r["Labels"]) == "" {
			l = nil
		}
		v := Volume{Name: str(r["Name"]), Driver: str(r["Driver"]), Scope: str(r["Scope"]), Mountpoint: str(r["Mountpoint"]),
			Labels: l, Project: l["com.docker.compose.project"], Stack: l["com.docker.stack.namespace"], Containers: []string{},
			Anonymous: len(str(r["Name"])) == 64 && l["com.docker.compose.volume"] == ""}
		// docker ps shortens long volume names in its Mounts column
		for name, cts := range users {
			if name == v.Name || (strings.HasSuffix(name, "…") && strings.HasPrefix(v.Name, strings.TrimSuffix(name, "…"))) {
				v.Containers = append(v.Containers, cts...)
			}
		}
		sort.Strings(v.Containers)
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Anonymous != list[j].Anonymous {
			return !list[i].Anonymous
		}
		return list[i].Name < list[j].Name
	})
	return list, nil
}

// VolumeCreate creates a local volume.
func (c *Client) VolumeCreate(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, name string) error {
	if !ValidID(name) {
		return fmt.Errorf("invalid volume name")
	}
	_, err := c.runOK(ctx, w, host, "volume create "+name)
	return err
}

// VolumeRemove removes a volume (it must not be in use).
func (c *Client) VolumeRemove(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, name string) error {
	if !ValidID(name) {
		return fmt.Errorf("invalid volume name")
	}
	_, err := c.runOK(ctx, w, host, "volume rm "+name)
	return err
}

// ---------------------------------------------------------------------------
// Browsing a volume's files.
//
// The data of a local volume lives under /var/lib/docker/volumes, usually
// readable only by root. opsdeck uses, in order: the host path directly
// (when the SSH user can read it), the host path with sudo -n, or a
// short-lived helper container that mounts the volume (needs only docker).
// Every operation runs a small POSIX script that resolves the requested path
// and refuses anything that leaves the volume (.., symlinks).

type volAccess struct {
	Mode  string // direct | sudo | container
	Root  string // path of the volume's data in that mode
	Image string // helper image (container mode)
}

// VolumeAccess says how a volume's files are reached.
func (c *Client) VolumeAccess(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, vol string) (string, error) {
	a, err := c.access(ctx, w, host, vol)
	return a.Mode, err
}

func (c *Client) access(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, vol string) (volAccess, error) {
	if !ValidID(vol) {
		return volAccess{}, fmt.Errorf("invalid volume name")
	}
	k := key(w, host) + "/" + vol
	c.mu.Lock()
	a, ok := c.vol[k]
	c.mu.Unlock()
	if ok {
		return a, nil
	}
	out, err := c.runOK(ctx, w, host, "volume inspect --format '{{.Driver}}|{{.Mountpoint}}' "+vol)
	if err != nil {
		return volAccess{}, err
	}
	driver, mp, _ := strings.Cut(strings.TrimSpace(out), "|")
	a = volAccess{Mode: "container", Root: "/v"}
	if driver == "local" && strings.HasPrefix(mp, "/") {
		q := shq(mp)
		if r, err := c.hub.Exec(ctx, w, host, "test -d "+q+" && test -r "+q+" && test -x "+q+" && test -w "+q, nil, 4096); err == nil && r.ExitCode == 0 {
			a = volAccess{Mode: "direct", Root: mp}
		} else if r, err := c.hub.Exec(ctx, w, host, "sudo -n test -d "+q, nil, 4096); err == nil && r.ExitCode == 0 {
			a = volAccess{Mode: "sudo", Root: mp}
		}
	}
	if a.Mode == "container" {
		a.Image = "busybox:stable"
		if imgs, err := c.Images(ctx, w, host); err == nil {
			for _, im := range imgs {
				if (im.Repository == "busybox" || im.Repository == "alpine") && im.Tag != "<none>" {
					a.Image = im.Repository + ":" + im.Tag
					break
				}
			}
		}
	}
	c.mu.Lock()
	c.vol[k] = a
	c.mu.Unlock()
	return a, nil
}

// volCmd wraps a script so it runs with the volume's data as $1.
func (c *Client) volCmd(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, vol string, a volAccess, script string, args ...string) (string, error) {
	q := make([]string, 0, len(args))
	for _, x := range args {
		q = append(q, shq(x))
	}
	tail := " sh -c " + shq(volPrelude+script) + " opsdeck " + shq(a.Root) + " " + strings.Join(q, " ")
	switch a.Mode {
	case "direct":
		return tail[1:], nil
	case "sudo":
		return "sudo -n" + tail, nil
	}
	p, err := c.Prefix(ctx, w, host)
	if err != nil {
		return "", err
	}
	return p + "run --rm -i --network none --log-driver none --label opsdeck.helper=volume -v " + vol + ":/v " + a.Image + tail, nil
}

// volPrelude resolves $root and defines inside() which stops on any path
// outside it.
const volPrelude = `set -u
root=$(cd "$1" && pwd -P) || exit 2
shift
inside() { case "$1" in "$root"|"$root"/*) return 0 ;; esac; echo "opsdeck: path leaves the volume" >&2; exit 3; }
resolve() { r=$(readlink -f -- "$root/$1") || { echo "opsdeck: not found: $1" >&2; exit 4; }; inside "$r"; }
`

// cleanRel normalises a path inside a volume ("" is the root).
func cleanRel(rel string) (string, error) {
	if strings.ContainsRune(rel, 0) {
		return "", errors.New("invalid path")
	}
	return strings.TrimPrefix(path.Clean("/"+rel), "/"), nil
}

func validName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") || len(name) > 255 {
		return fmt.Errorf("invalid file name %q", name)
	}
	return nil
}

// VolumeEntry is a file in a volume.
type VolumeEntry struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // file | dir | link | other
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	Mode     string `json:"mode"`
	Owner    string `json:"owner"`
}

// VolumeList lists a directory of a volume.
func (c *Client) VolumeList(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, vol, rel string) (mode string, entries []VolumeEntry, err error) {
	a, err := c.access(ctx, w, host, vol)
	if err != nil {
		return "", nil, err
	}
	if rel, err = cleanRel(rel); err != nil {
		return "", nil, err
	}
	cmd, err := c.volCmd(ctx, w, host, vol, a, `resolve "$1"; [ -d "$r" ] || { echo "opsdeck: not a directory" >&2; exit 5; }
cd "$r" || exit 6
for f in * .[!.]* ..?*; do
  [ -e "$f" ] || [ -L "$f" ] || continue
  stat -c '%F|%s|%Y|%a|%U:%G|%n' -- "$f"
done | head -n 5000`, rel)
	if err != nil {
		return "", nil, err
	}
	res, err := c.hub.Exec(ctx, w, host, cmd, nil, 4<<20)
	if err != nil {
		return a.Mode, nil, err
	}
	if res.ExitCode != 0 {
		return a.Mode, nil, volErr(res.Stderr, res.ExitCode)
	}
	entries = []VolumeEntry{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		f := strings.SplitN(line, "|", 6)
		if len(f) < 6 {
			continue
		}
		size, _ := strconv.ParseInt(f[1], 10, 64)
		sec, _ := strconv.ParseInt(f[2], 10, 64)
		t := "other"
		switch {
		case strings.Contains(f[0], "directory"):
			t = "dir"
		case strings.Contains(f[0], "symbolic link"):
			t = "link"
		case strings.Contains(f[0], "regular"):
			t = "file"
		}
		entries = append(entries, VolumeEntry{Name: f[5], Type: t, Size: size, Modified: time.Unix(sec, 0).UTC().Format(time.RFC3339), Mode: f[3], Owner: f[4]})
	}
	sort.Slice(entries, func(i, j int) bool {
		if (entries[i].Type == "dir") != (entries[j].Type == "dir") {
			return entries[i].Type == "dir"
		}
		return entries[i].Name < entries[j].Name
	})
	return a.Mode, entries, nil
}

func volErr(stderr string, code int) error {
	msg := strings.TrimSpace(stderr)
	if i := strings.LastIndex(msg, "opsdeck: "); i >= 0 {
		msg = strings.TrimSpace(msg[i+len("opsdeck: "):])
	}
	if msg == "" {
		msg = fmt.Sprintf("exit code %d", code)
	}
	return errors.New(msg)
}

// VolumeRead streams a file of a volume to out (max bytes when max > 0).
func (c *Client) VolumeRead(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, vol, rel string, max int64, out io.Writer) error {
	a, err := c.access(ctx, w, host, vol)
	if err != nil {
		return err
	}
	if rel, err = cleanRel(rel); err != nil {
		return err
	}
	read := `cat -- "$r"`
	if max > 0 {
		read = "head -c " + strconv.FormatInt(max, 10) + ` -- "$r"`
	}
	cmd, err := c.volCmd(ctx, w, host, vol, a, `resolve "$1"; [ -f "$r" ] || { echo "opsdeck: not a file" >&2; exit 5; }
`+read, rel)
	if err != nil {
		return err
	}
	code, stderr, err := c.hub.ExecIO(ctx, w, host, cmd, nil, out, 64<<10)
	if err != nil {
		return err
	}
	if code != 0 {
		return volErr(stderr, code)
	}
	return nil
}

// VolumeWrite stores data as dir/name in a volume (replacing a file of that
// name). The new file gets the owner of its directory, so the containers
// using the volume can read it.
func (c *Client) VolumeWrite(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, vol, dir, name string, data io.Reader) error {
	a, err := c.access(ctx, w, host, vol)
	if err != nil {
		return err
	}
	if dir, err = cleanRel(dir); err != nil {
		return err
	}
	if err := validName(name); err != nil {
		return err
	}
	cmd, err := c.volCmd(ctx, w, host, vol, a, `resolve "$1"; [ -d "$r" ] || { echo "opsdeck: not a directory" >&2; exit 5; }
t="$r/$2"
[ -d "$t" ] && [ ! -L "$t" ] && { echo "opsdeck: $2 is a directory" >&2; exit 7; }
tmp="$r/.opsdeck-upload.$$"
cat > "$tmp" || { rm -f "$tmp"; exit 8; }
if [ -f "$t" ] && [ ! -L "$t" ]; then chmod "$(stat -c %a "$t")" "$tmp"; chown "$(stat -c %u:%g "$t")" "$tmp" 2>/dev/null
else chmod 644 "$tmp"; chown "$(stat -c %u:%g "$r")" "$tmp" 2>/dev/null; fi
mv -f "$tmp" "$t"`, dir, name)
	if err != nil {
		return err
	}
	code, stderr, err := c.hub.ExecIO(ctx, w, host, cmd, data, nil, 64<<10)
	if err != nil {
		return err
	}
	if code != 0 {
		return volErr(stderr, code)
	}
	return nil
}

// VolumeMkdir creates dir/name in a volume.
func (c *Client) VolumeMkdir(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, vol, dir, name string) error {
	if err := validName(name); err != nil {
		return err
	}
	return c.volSimple(ctx, w, host, vol, dir, `resolve "$1"; [ -d "$r" ] || { echo "opsdeck: not a directory" >&2; exit 5; }
mkdir -- "$r/$2" && chown "$(stat -c %u:%g "$r")" "$r/$2" 2>/dev/null; [ -d "$r/$2" ]`, name)
}

// VolumeDelete removes a file or directory (recursively) of a volume; a
// symlink is removed itself, never its target.
func (c *Client) VolumeDelete(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, vol, rel string) error {
	rel, err := cleanRel(rel)
	if err != nil {
		return err
	}
	if rel == "" {
		return errors.New("refusing to delete the volume's root")
	}
	return c.volSimple(ctx, w, host, vol, path.Dir(rel), `resolve "$1"; t="$r/$2"
[ -e "$t" ] || [ -L "$t" ] || { echo "opsdeck: not found" >&2; exit 4; }
rm -rf -- "$t"`, path.Base(rel))
}

// VolumeSize returns the disk usage of a volume in bytes.
func (c *Client) VolumeSize(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, vol string) (int64, error) {
	a, err := c.access(ctx, w, host, vol)
	if err != nil {
		return 0, err
	}
	cmd, err := c.volCmd(ctx, w, host, vol, a, `du -sk "$root" | cut -f1`)
	if err != nil {
		return 0, err
	}
	res, err := c.hub.Exec(ctx, w, host, cmd, nil, 64<<10)
	if err != nil {
		return 0, err
	}
	if res.ExitCode != 0 {
		return 0, volErr(res.Stderr, res.ExitCode)
	}
	kb, _ := strconv.ParseInt(strings.TrimSpace(res.Stdout), 10, 64)
	return kb * 1024, nil
}

func (c *Client) volSimple(ctx context.Context, w *workspace.Workspace, host *workspace.Resource, vol, rel, script string, args ...string) error {
	a, err := c.access(ctx, w, host, vol)
	if err != nil {
		return err
	}
	if rel, err = cleanRel(rel); err != nil {
		return err
	}
	cmd, err := c.volCmd(ctx, w, host, vol, a, script, append([]string{rel}, args...)...)
	if err != nil {
		return err
	}
	res, err := c.hub.Exec(ctx, w, host, cmd, nil, 64<<10)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return volErr(res.Stderr, res.ExitCode)
	}
	return nil
}
