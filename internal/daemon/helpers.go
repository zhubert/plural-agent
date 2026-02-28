package daemon

import (
	"fmt"

	"github.com/zhubert/erg/internal/config"
)

// getSessionOrError retrieves the session for the given ID or returns a
// descriptive error. This consolidates the repeated nil-check pattern used
// across action and github_ops handlers that return an error on missing sessions.
func (d *Daemon) getSessionOrError(sessionID string) (*config.Session, error) {
	sess := d.config.GetSession(sessionID)
	if sess == nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	return sess, nil
}
