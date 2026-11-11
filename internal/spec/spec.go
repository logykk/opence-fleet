// Package spec is a node's desired state, the types the control plane and
// the agent share, and how an instance's files are made from a profile and
// its overrides: config.toml (the game settings, changed live through the
// server's API), init.txt, the gametype files and the environment (the
// fleet's infrastructure settings, which the server pins). It is pure:
// rendering takes values and returns text, so its tests are golden files.
package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Ports is a node's set of ports: a range, or whichever ports the machine's
// host handed it (a hosting platform promises no contiguous block, so the
// fleet assumes none). Parsing and decoding keep it sorted, without repeats.
type Ports []int

// PortRange is the ports from first to last.
func PortRange(first, last int) Ports {
	if first < 0 || last > 65535 || last < first {
		return nil
	}
	out := make(Ports, 0, last-first+1)
	for p := first; p <= last; p++ {
		out = append(out, p)
	}
	return out
}

// Contains tells whether a port is in the set.
func (p Ports) Contains(port int) bool { return slices.Contains(p, port) }

// Overlaps tells whether two sets share a port.
func (p Ports) Overlaps(other Ports) bool {
	seen := make(map[int]bool, len(p))
	for _, port := range p {
		seen[port] = true
	}
	for _, port := range other {
		if seen[port] {
			return true
		}
	}
	return false
}

// contiguous tells whether the set is one range, first to last.
func (p Ports) contiguous() bool {
	if len(p) == 0 {
		return false
	}
	for k, port := range p {
		if port != p[0]+k {
			return false
		}
	}
	return true
}

// String writes the set as ParsePorts reads it: "2302-2399", or
// "12312, 32212, 57211" (runs of neighbours as ranges).
func (p Ports) String() string {
	var parts []string
	for k := 0; k < len(p); {
		end := k
		for end+1 < len(p) && p[end+1] == p[end]+1 {
			end++
		}
		if end > k {
			parts = append(parts, fmt.Sprintf("%d-%d", p[k], p[end]))
		} else {
			parts = append(parts, strconv.Itoa(p[k]))
		}
		k = end + 1
	}
	return strings.Join(parts, ", ")
}

// portRange is the JSON of a contiguous set, as every fleet before lists
// wrote it.
type portRange struct {
	First int `json:"first"`
	Last  int `json:"last"`
}

// MarshalJSON writes a range as {"first": 2302, "last": 2399} and any other
// set as a list, [12312, 32212, 57211]. (A range keeps its old shape so an
// older agent still reads its spec: one that predates lists ignores a spec
// with a list until it is updated.)
func (p Ports) MarshalJSON() ([]byte, error) {
	if p.contiguous() {
		return json.Marshal(portRange{p[0], p[len(p)-1]})
	}
	return json.Marshal([]int(p))
}

// UnmarshalJSON reads either shape MarshalJSON writes.
func (p *Ports) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	switch {
	case bytes.Equal(data, []byte("null")):
		return nil
	case len(data) > 0 && data[0] == '{':
		var r portRange
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		if r.First < 0 || r.Last > 65535 || r.Last < r.First {
			return fmt.Errorf("ports %d to %d: ports from 1024 to 65535, the first not past the last", r.First, r.Last)
		}
		*p = PortRange(r.First, r.Last)
		return nil
	}
	var list []int
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("ports are a range ({\"first\": 2302, \"last\": 2399}) or a list ([12312, 32212]): %w", err)
	}
	*p = normalPorts(list)
	return nil
}

func normalPorts(list []int) Ports {
	out := Ports(slices.Clone(list))
	slices.Sort(out)
	return slices.Compact(out)
}

// ParsePorts reads ranges and ports by commas: "2302-2399",
// "12312, 32212, 57211" or "2302-2309, 2400".
func ParsePorts(text string) (Ports, error) {
	var list []int
	for _, item := range strings.Split(text, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		first, last, found := strings.Cut(item, "-")
		a, err := strconv.Atoi(strings.TrimSpace(first))
		b := a
		if err == nil && found {
			b, err = strconv.Atoi(strings.TrimSpace(last))
		}
		if err != nil {
			return nil, fmt.Errorf("%q is not a port or a range: write 2302-2399, or ports by commas (12312, 32212)", item)
		}
		if a < 1024 || b > 65535 || b < a {
			return nil, fmt.Errorf("%q: ports from 1024 to 65535, the first not past the last", item)
		}
		list = append(list, PortRange(a, b)...)
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("no ports: write a range (2302-2399), or ports by commas (12312, 32212)")
	}
	return normalPorts(list), nil
}

// Node is a node's settings, which the control plane keeps (the node page
// changes them) and sends in each spec.
type Node struct {
	// docker or process ("": the agent's own FLEET_AGENT_RUNTIME)
	Runtime     string `json:"runtime,omitempty"`
	TunnelPorts Ports  `json:"tunnel_ports"`
	WebPorts    Ports  `json:"web_ports"`
	// the process runtime's first loopback address, 127.0.0.<base>
	LoopbackBase  int    `json:"loopback_base"`
	PublicAddress string `json:"public_address,omitempty"`
	// each container's limits (the process runtime: none per game)
	MemoryMB int     `json:"memory_mb"`
	CPUs     float64 `json:"cpus"`
	// containers on the host's network (system link on the LAN)
	HostNetwork bool `json:"host_network,omitempty"`
	// the server's own page on its web port
	WebUI bool `json:"web_ui,omitempty"`
	// sync the whole library, not only what the node's servers play
	SyncAll bool `json:"sync_all,omitempty"`
}

// DefaultNode is a new node's settings.
func DefaultNode() Node {
	return Node{TunnelPorts: PortRange(2302, 2399), WebPorts: PortRange(8101, 8199), LoopbackBase: 2,
		MemoryMB: 600, CPUs: 1}
}

// Check refuses settings that cannot work.
func (n Node) Check() error {
	switch n.Runtime {
	case "", "docker", "process":
	default:
		return fmt.Errorf("the runtime is docker or process")
	}
	for _, set := range []struct {
		name  string
		ports Ports
	}{{"tunnel", n.TunnelPorts}, {"web", n.WebPorts}} {
		if len(set.ports) == 0 {
			return fmt.Errorf("the node has no %s ports: give a range (2302-2399) or a list of ports", set.name)
		}
		for _, port := range set.ports {
			if port < 1024 || port > 65535 {
				return fmt.Errorf("%s port %d: ports from 1024 to 65535", set.name, port)
			}
		}
	}
	if n.TunnelPorts.Overlaps(n.WebPorts) {
		return fmt.Errorf("the tunnel ports (%s) and the web ports (%s) overlap", n.TunnelPorts, n.WebPorts)
	}
	if n.LoopbackBase < 2 || n.LoopbackBase > 250 {
		return fmt.Errorf("the loopback base is 2 to 250 (127.0.0.<base>)")
	}
	if n.MemoryMB != 0 && n.MemoryMB < 128 {
		return fmt.Errorf("a server needs 128 MB at least (0: no limit)")
	}
	if n.CPUs < 0 || n.CPUs > 64 {
		return fmt.Errorf("CPUs: 0 (no limit) to 64")
	}
	return nil
}

// Spec is a node's desired state, sent whole on every change.
type Spec struct {
	Generation int64         `json:"generation"`
	Node       Node          `json:"node"`
	Images     []Image       `json:"images"`
	Releases   []Release     `json:"releases"`
	Library    []LibraryItem `json:"library"`
	Instances  []Instance    `json:"instances"`
}

// Image is a release as the server image: a tag and the digest the control
// plane resolved it to (empty for an image built on the node).
type Image struct {
	Build  string `json:"build"`
	Ref    string `json:"ref"`
	Digest string `json:"digest,omitempty"`
}

// Pull is what a node pulls and runs: the reference pinned to its digest.
func (i Image) Pull() string {
	if i.Digest == "" {
		return i.Ref
	}
	repository := i.Ref
	// (the tag goes: a reference with a digest is the digest's)
	if slash := strings.LastIndexByte(repository, '/'); strings.LastIndexByte(repository, ':') > slash {
		repository = repository[:strings.LastIndexByte(repository, ':')]
	}
	return repository + "@" + i.Digest
}

// Release is a release as halo-linux-release.zip, for the process runtime.
type Release struct {
	Build  string `json:"build"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	// the control plane's /agent/releases/<build>.zip
	URL string `json:"url"`
}

// LibraryItem is a file of the game data, by its hash.
type LibraryItem struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Folder is where an item goes in the data root: maps/<name>.map,
// custom_maps/<name>.map, gametypes/<name>.toml (or .gametype).
func (l LibraryItem) Folder() string {
	switch l.Kind {
	case "map":
		return "maps"
	case "custom_map", "resource":
		return "custom_maps"
	case "gametype", "saved_gametype":
		return "gametypes"
	}
	return "files"
}

// FileName is the item's file name in its folder.
func (l LibraryItem) FileName() string {
	switch l.Kind {
	case "map", "custom_map", "resource":
		return l.Name + ".map"
	case "gametype":
		return l.Name + ".toml"
	case "saved_gametype":
		return l.Name + ".gametype"
	}
	return l.Name
}

// Path is the item's path under the data root.
func (l LibraryItem) Path() string { return l.Folder() + "/" + l.FileName() }

// Instance is one server's desired state.
type Instance struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Generation int64  `json:"generation"`
	Build      string `json:"build"`
	Running    bool   `json:"running"`
	TunnelPort int    `json:"tunnel_port"`
	WebPort    int    `json:"web_port"`
	// the process runtime's loopback address, 127.0.0.<Loopback>
	Loopback int `json:"loopback"`
	// the fleet's settings (HALO_*), pinned: the runtime adds its paths
	Env map[string]string `json:"env"`
	// the agent's sign-in to the server's web port (the admin's; the
	// owner's with the game's owner role over the web)
	WebPassword   string `json:"web_password"`
	OwnerPassword string `json:"owner_password,omitempty"`
	// the game settings: config.toml's text, and the same as values for
	// changing them live
	ConfigTOML string            `json:"config_toml"`
	Settings   map[string]any    `json:"settings"`
	Rotation   string            `json:"rotation"`
	InitTxt    string            `json:"init_txt"`
	Gametypes  map[string]string `json:"gametypes"`
	// the library items its rotation needs (hashes)
	Needs []string `json:"needs"`
}

// RestartKey is what of an instance needs a restart to change: its build,
// ports, environment and init.txt. Two specs with the same key differ only
// in what the server changes live.
func (i Instance) RestartKey() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%d|%d|%d|%s|%s|", i.Build, i.TunnelPort, i.WebPort, i.Loopback, i.InitTxt, i.OwnerPassword)
	keys := make([]string, 0, len(i.Env))
	for k := range i.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, i.Env[k])
	}
	return b.String()
}

// Setting is a game setting of the table (settings_table.go).
type Setting struct {
	Name    string
	Type    string // string, integer, real, boolean
	Default any
	Env     string
	// changed live by the server (sv_set, PUT /api/settings)
	Live    bool
	Group   string
	Choices []string
	Min     *int
	Max     *int
	Secret  bool
	Comment string
}

func ptr(n int) *int { return &n }

// Lookup finds a setting of the table.
func Lookup(name string) (Setting, bool) {
	for _, s := range Settings {
		if s.Name == name {
			return s, true
		}
	}
	return Setting{}, false
}

// Groups is the settings' groups, in the table's order.
func Groups() []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range Settings {
		if !seen[s.Group] {
			seen[s.Group] = true
			out = append(out, s.Group)
		}
	}
	return out
}

var printable = regexp.MustCompile(`^[\x20-\x7e]*$`)

// Normalize checks a setting's value and returns it as its type (from a
// form's text, or JSON's bool, float64 or string), as the server would take
// it, with the server's words for a refusal.
func Normalize(name string, value any) (any, error) {
	s, ok := Lookup(name)
	if !ok {
		return nil, fmt.Errorf("%s is not a setting a profile sets", name)
	}
	text := ""
	switch v := value.(type) {
	case string:
		text = strings.TrimSpace(v)
	case bool:
		text = strconv.FormatBool(v)
	case float64:
		text = strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		text = strconv.Itoa(v)
	case int64:
		text = strconv.FormatInt(v, 10)
	case nil:
		return s.Default, nil
	default:
		return nil, fmt.Errorf("%s: a value of the wrong type", name)
	}
	switch s.Type {
	case "boolean":
		switch strings.ToLower(text) {
		case "true", "1", "on", "yes":
			return true, nil
		case "false", "0", "off", "no", "":
			return false, nil
		}
		return nil, fmt.Errorf("%s is true or false", name)
	case "integer":
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			if f, ferr := strconv.ParseFloat(text, 64); ferr == nil && f == math.Trunc(f) {
				n, err = int64(f), nil
			}
		}
		if err != nil || (s.Min != nil && n < int64(*s.Min)) || (s.Max != nil && n > int64(*s.Max)) {
			switch {
			case s.Min != nil && s.Max != nil:
				return nil, fmt.Errorf("%s takes a whole number from %d to %d", name, *s.Min, *s.Max)
			case s.Min != nil:
				return nil, fmt.Errorf("%s takes a whole number from %d", name, *s.Min)
			}
			return nil, fmt.Errorf("%s takes a whole number", name)
		}
		return n, nil
	case "real":
		f, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%s takes a number", name)
		}
		return f, nil
	}
	if len(s.Choices) > 0 {
		for _, c := range s.Choices {
			if strings.EqualFold(c, text) {
				return c, nil
			}
		}
		return nil, fmt.Errorf("%s is one of %s", name, strings.Join(s.Choices, "|"))
	}
	if !printable.MatchString(text) {
		return nil, fmt.Errorf("%s takes printable ASCII", name)
	}
	if len(text) > 256 {
		return nil, fmt.Errorf("%s is 256 characters at most", name)
	}
	if name == "server.name" && len(text) > 15 {
		// (the game shows 15; the server cuts the rest)
		text = text[:15]
	}
	return text, nil
}

// SettingText writes a setting's value for a form.
func SettingText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	}
	return fmt.Sprint(value)
}

// Merge is an instance's game settings: the profile's, then the instance's
// overrides over them. Values are normalized; one that does not normalize
// is left out.
func Merge(profile, overrides map[string]any) map[string]any {
	out := map[string]any{}
	for _, layer := range []map[string]any{profile, overrides} {
		for name, value := range layer {
			if v, err := Normalize(name, value); err == nil {
				out[name] = v
			}
		}
	}
	return out
}

// Equal tells whether two settings' values are the same.
func Equal(a, b any) bool { return SettingText(a) == SettingText(b) }
