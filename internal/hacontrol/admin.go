package hacontrol

import (
	"context"
	"encoding/json"
	"regexp"
)

var userIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// AuthorizeAdmin resolves only the Supervisor-injected ID. A dedicated socket
// avoids competition with discovery. No cache can outlive role revocation.
// Names, credentials and other users never leave this adapter.
func (n *Native) AuthorizeAdmin(ctx context.Context, id string) error {
	if !userIDPattern.MatchString(id) {
		return ErrCore
	}
	raw, _, err := n.adminCore.command(ctx, "config/auth/list", 256<<10)
	if err != nil {
		return ErrCore
	}
	var users []struct {
		ID     string   `json:"id"`
		Active bool     `json:"is_active"`
		Owner  bool     `json:"is_owner"`
		System bool     `json:"system_generated"`
		Groups []string `json:"group_ids"`
	}
	if json.Unmarshal(raw, &users) != nil || len(users) > 512 {
		return ErrCore
	}
	found, allowed := false, false
	seen := make(map[string]bool, len(users))
	for _, u := range users {
		if !userIDPattern.MatchString(u.ID) || seen[u.ID] || len(u.Groups) > 32 {
			return ErrCore
		}
		seen[u.ID] = true
		if u.ID != id {
			continue
		}
		found = true
		admin := u.Owner
		for _, g := range u.Groups {
			if g == "system-admin" {
				admin = true
			}
		}
		allowed = u.Active && !u.System && admin
	}
	if !found || !allowed {
		return ErrCore
	}
	return nil
}
