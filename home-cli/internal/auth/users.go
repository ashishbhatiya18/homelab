package auth

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// User is one entry of users.yml. The password is an argon2id digest.
type User struct {
	Name        string
	DisplayName string
	Email       string
	Groups      []string
	Disabled    bool
}

// Users is a parsed users.yml.
type Users struct{ d *doc }

func ParseUsers(b []byte) (*Users, error) {
	d, err := parse(b, "users.yml")
	if err != nil {
		return nil, err
	}
	return &Users{d: d}, nil
}

func (u *Users) Bytes() []byte { return u.d.bytes() }

func (u *Users) users() *yaml.Node { return child(u.d.root, "users") }

func (u *Users) entry(name string) *yaml.Node {
	if e := get(u.users(), name); e != nil && e.Kind == yaml.MappingNode {
		return e
	}
	return nil
}

// List returns every user, sorted by name.
func (u *Users) List() []User {
	m := u.users()
	var out []User
	for i := 0; i+1 < len(m.Content); i += 2 {
		e := m.Content[i+1]
		out = append(out, User{
			Name:        m.Content[i].Value,
			DisplayName: scalar(e, "displayname"),
			Email:       scalar(e, "email"),
			Groups:      values(get(e, "groups")),
			Disabled:    scalar(e, "disabled") == "true",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (u *Users) Get(name string) (User, bool) {
	for _, x := range u.List() {
		if x.Name == name {
			return x, true
		}
	}
	return User{}, false
}

// Add appends a new user with the given password digest.
func (u *Users) Add(x User, digest string) error {
	if err := ValidName(x.Name); err != nil {
		return err
	}
	if u.entry(x.Name) != nil {
		return fmt.Errorf("user %q already exists", x.Name)
	}
	if x.Email == "" {
		return fmt.Errorf("user %q needs an email", x.Name)
	}
	if err := u.emailFree(x.Email, x.Name); err != nil {
		return err
	}
	if x.DisplayName == "" {
		x.DisplayName = x.Name
	}
	e := &yaml.Node{Kind: yaml.MappingNode}
	set(e, "displayname", quoted(x.DisplayName))
	set(e, "password", quoted(digest))
	set(e, "email", quoted(x.Email))
	set(e, "groups", flowList(x.Groups))
	if x.Disabled {
		set(e, "disabled", boolean(true))
	}
	m := u.users()
	m.Content = append(m.Content, str(x.Name), e)
	return nil
}

// Change is a partial update; nil fields stay as they are.
type Change struct {
	DisplayName, Email, Password *string // Password: a digest
	Groups                       *[]string
	Disabled                     *bool
}

func (u *Users) Set(name string, c Change) error {
	e := u.entry(name)
	if e == nil {
		return fmt.Errorf("no user %q", name)
	}
	if c.DisplayName != nil {
		set(e, "displayname", quoted(*c.DisplayName))
	}
	if c.Email != nil {
		if *c.Email == "" {
			return fmt.Errorf("user %q needs an email", name)
		}
		if err := u.emailFree(*c.Email, name); err != nil {
			return err
		}
		set(e, "email", quoted(*c.Email))
	}
	if c.Password != nil {
		set(e, "password", quoted(*c.Password))
	}
	if c.Groups != nil {
		set(e, "groups", flowList(*c.Groups))
	}
	if c.Disabled != nil {
		if *c.Disabled {
			set(e, "disabled", boolean(true))
		} else {
			del(e, "disabled")
		}
	}
	return nil
}

// emailFree refuses an email another user already has: apps such as Immich
// link accounts by email, so two Authelia users with one email are ambiguous.
func (u *Users) emailFree(email, self string) error {
	for _, x := range u.List() {
		if x.Name != self && strings.EqualFold(x.Email, email) {
			return fmt.Errorf("%s already belongs to user %q", email, x.Name)
		}
	}
	return nil
}

func (u *Users) Remove(name string) error {
	if !del(u.users(), name) {
		return fmt.Errorf("no user %q", name)
	}
	return nil
}
