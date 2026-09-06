package config

import (
	"bytes"
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

// Editing an existing config file goes through yaml.Node rather than a
// marshal of the whole Config: a user's comments, key order and unrelated
// sections have to survive an account being added, changed or removed.

// accountDoc is the account as it is written back out. The load-facing
// Account has no omitempty tags (an absent key and an empty one mean the same
// thing on the way in), which would write "smtp: null" on the way out.
type accountDoc struct {
	Name     string    `yaml:"name"`
	Host     string    `yaml:"host"`
	Port     int       `yaml:"port"`
	Username string    `yaml:"username"`
	Auth     string    `yaml:"auth"`
	Folders  []string  `yaml:"folders,omitempty"`
	Trash    string    `yaml:"trash,omitempty"`
	SMTP     *smtpDoc  `yaml:"smtp,omitempty"`
	OAuth    *oauthDoc `yaml:"oauth,omitempty"`
}

type smtpDoc struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type oauthDoc struct {
	Provider     string   `yaml:"provider"`
	ClientID     string   `yaml:"client_id"`
	ClientSecret string   `yaml:"client_secret,omitempty"`
	Tenant       string   `yaml:"tenant,omitempty"`
	AuthURL      string   `yaml:"auth_url,omitempty"`
	TokenURL     string   `yaml:"token_url,omitempty"`
	Scopes       []string `yaml:"scopes,omitempty"`
}

func toDoc(a Account) accountDoc {
	d := accountDoc{
		Name: a.Name, Host: a.Host, Port: a.Port, Username: a.Username,
		Auth: nonBlank(a.Auth, "file"), Folders: a.Folders, Trash: a.Trash,
	}
	if d.Port == 0 {
		d.Port = 993
	}
	if a.SMTP != nil && a.SMTP.Host != "" {
		d.SMTP = &smtpDoc{Host: a.SMTP.Host, Port: a.SMTP.Port}
	}
	if a.Auth == "oauth" && a.OAuth != nil {
		o := *a.OAuth
		if o.Provider == "" {
			o.Provider = OAuthProviderFor(a.Provider())
		}
		d.OAuth = &oauthDoc{
			Provider: o.Provider, ClientID: o.ClientID, ClientSecret: o.ClientSecret,
			Tenant: o.Tenant, AuthURL: o.AuthURL, TokenURL: o.TokenURL, Scopes: o.Scopes,
		}
	}
	return d
}

// UpsertAccount writes a into the config file at path, replacing the account
// with the same name or appending it to the list. Comments and every other
// key survive. Only a file that is not there yet is written fresh: a file
// that exists but does not parse, or parses without an accounts: list, is
// reported rather than overwritten, since overwriting it would throw away
// whatever the owner had put in it.
func UpsertAccount(path string, a Account, protect Protect) error {
	doc, err := readDoc(path)
	if err != nil {
		return err
	}
	if doc == nil {
		return WriteInitial(path, a, protect)
	}
	accounts, err := accountsNode(doc, path)
	if err != nil {
		return err
	}

	node, err := encodeAccount(a)
	if err != nil {
		return err
	}
	if i := indexOfAccount(accounts, a.Name); i >= 0 {
		mergeMapping(accounts.Content[i], node, ownedAccountKey)
	} else {
		accounts.Content = append(accounts.Content, node)
	}
	return writeDoc(path, doc)
}

// ownedAccountKey reports the keys UpsertAccount is responsible for. One of
// these that the new account no longer carries (the oauth: block after a
// switch back to a password) is dropped on an edit; anything else in the
// block is the owner's and is left where it is.
func ownedAccountKey(key string) bool {
	switch key {
	case "name", "host", "port", "username", "auth", "folders", "trash", "smtp", "oauth":
		return true
	}
	return false
}

// mergeMapping writes src's keys into dst in place rather than replacing dst,
// so a comment the owner wrote inside the account block survives an edit.
// Values are replaced (carrying the old node's comments over when the new one
// has none), missing keys are appended at the end, and a key owned reports as
// this program's that src does not have is removed. Nested blocks are entirely
// this program's, so everything in them is owned.
func mergeMapping(dst, src *yaml.Node, owned func(string) bool) {
	for i := 0; i+1 < len(src.Content); i += 2 {
		key, val := src.Content[i], src.Content[i+1]
		j := indexOfKey(dst, key.Value)
		if j < 0 {
			dst.Content = append(dst.Content, key, val)
			continue
		}
		if dst.Content[j+1].Kind == yaml.MappingNode && val.Kind == yaml.MappingNode {
			mergeMapping(dst.Content[j+1], val, func(string) bool { return true })
			continue
		}
		carryComments(dst.Content[j], key)
		carryComments(dst.Content[j+1], val)
		dst.Content[j+1] = val
	}

	kept := dst.Content[:0]
	for i := 0; i+1 < len(dst.Content); i += 2 {
		if owned(dst.Content[i].Value) && indexOfKey(src, dst.Content[i].Value) < 0 {
			continue
		}
		kept = append(kept, dst.Content[i], dst.Content[i+1])
	}
	dst.Content = kept
}

// carryComments moves the comments off the node being replaced onto the one
// replacing it, which has none of its own: a freshly encoded value would
// otherwise drop the explanation written next to the key it overwrites.
func carryComments(old, fresh *yaml.Node) {
	if fresh.HeadComment == "" {
		fresh.HeadComment = old.HeadComment
	}
	if fresh.LineComment == "" {
		fresh.LineComment = old.LineComment
	}
	if fresh.FootComment == "" {
		fresh.FootComment = old.FootComment
	}
}

// indexOfKey returns the index of key in a mapping node, or -1.
func indexOfKey(mapping *yaml.Node, key string) int {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// RemoveAccount drops the named account from the config file. Nothing else in
// the file changes, and no mail or stored data is touched.
func RemoveAccount(path, name string) error {
	doc, err := readDoc(path)
	if err != nil {
		return err
	}
	if doc == nil {
		return fmt.Errorf("%s: no config file", path)
	}
	accounts, err := accountsNode(doc, path)
	if err != nil {
		return err
	}
	i := indexOfAccount(accounts, name)
	if i < 0 {
		return fmt.Errorf("%s: account %q not found", path, name)
	}
	accounts.Content = append(accounts.Content[:i:i], accounts.Content[i+1:]...)
	return writeDoc(path, doc)
}

// readDoc parses the config file into a node tree. A missing file is (nil,
// nil): the caller decides whether that is an error or a fresh start.
func readDoc(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%s is empty; delete it and mailshear will write a new one, or fill it in by hand", path)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s does not parse as YAML (%v); fix it by hand, mailshear will not overwrite it", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("%s does not parse as YAML (empty document); fix it by hand, mailshear will not overwrite it", path)
	}
	return &doc, nil
}

func writeDoc(path string, doc *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// accountsNode finds the accounts: sequence. A file without one is not a
// config this program wrote, so it is reported rather than rewritten: adding
// the key would mean guessing at what the rest of the file is for.
func accountsNode(doc *yaml.Node, path string) (*yaml.Node, error) {
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: the top level is not a mapping; fix it by hand, mailshear will not overwrite it", path)
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "accounts" {
			n := root.Content[i+1]
			if n.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("%s: accounts: is not a list; fix it by hand, mailshear will not overwrite it", path)
			}
			return n, nil
		}
	}
	return nil, fmt.Errorf("%s: no accounts: list; add one (or delete the file and let mailshear write a new one), mailshear will not overwrite it", path)
}

func indexOfAccount(accounts *yaml.Node, name string) int {
	for i, acct := range accounts.Content {
		if acct.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(acct.Content); j += 2 {
			if acct.Content[j].Value == "name" && acct.Content[j+1].Value == name {
				return i
			}
		}
	}
	return -1
}

// encodeAccount renders one account as a mapping node, with the folder list
// inline so the file still reads like the shipped example.
func encodeAccount(a Account) (*yaml.Node, error) {
	var node yaml.Node
	if err := node.Encode(toDoc(a)); err != nil {
		return nil, fmt.Errorf("config: encoding account %q: %w", a.Name, err)
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == "folders" {
			node.Content[i+1].Style = yaml.FlowStyle
		}
	}
	return &node, nil
}
