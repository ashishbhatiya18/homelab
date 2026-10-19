package auth

import "strings"

// Passkey is a WebAuthn credential from `authelia storage user webauthn list`.
type Passkey struct{ ID, RPID, Description, Username string }

// ParsePasskeys reads the table `authelia storage user webauthn list` prints:
// columns ID, RPID, KID, Description, Username. A description may contain
// spaces, so it is everything between KID and the last column.
func ParsePasskeys(out string) []Passkey {
	var ps []Passkey
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 5 || f[0] == "ID" || strings.Trim(f[0], "0123456789") != "" {
			continue
		}
		ps = append(ps, Passkey{
			ID:          f[0],
			RPID:        f[1],
			Description: strings.Join(f[3:len(f)-1], " "),
			Username:    f[len(f)-1],
		})
	}
	return ps
}
