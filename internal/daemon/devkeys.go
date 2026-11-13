package daemon

import (
	"context"
	"errors"

	"palm-gw/internal/ctl"
)

// The Palms' device keys (D-063): issued at a login with Remember, checked
// at a login by key, listed and revoked on the settings page and with
// palm-gwctl devkeys. Whether the key's account still exists is the web
// UI's check, which knows the accounts.

func (d *Daemon) handleDevKeyIssue(_ context.Context, req ctl.Request, w *ctl.Writer) error {
	k := req.DevKey
	if k == nil {
		return errors.New("devkey.issue: for whom?")
	}
	key, e, err := d.devkeys.Issue(k.User, k.Role, k.Name)
	if err != nil {
		return err
	}
	d.log.Info("device key issued", "event", "devkey_issued", "id", e.ID, "user", e.User, "role", e.Role, "name", e.Name)
	return w.Send(ctl.DevKeyReply{Key: key, Entry: e})
}

func (d *Daemon) handleDevKeyCheck(_ context.Context, req ctl.Request, w *ctl.Writer) error {
	if req.DevKey == nil {
		return errors.New("devkey.check: no key")
	}
	e, err := d.devkeys.Check(req.DevKey.Key, req.DevKey.IP)
	if err != nil {
		return err
	}
	return w.Send(ctl.DevKeyReply{Entry: e})
}

func (d *Daemon) handleDevKeyList(_ context.Context, _ ctl.Request, w *ctl.Writer) error {
	return w.Send(ctl.DevKeysReply{Keys: d.devkeys.List()})
}

func (d *Daemon) handleDevKeyRevoke(_ context.Context, req ctl.Request, w *ctl.Writer) error {
	if req.DevKey == nil || req.DevKey.ID == "" {
		return errors.New("devkey.revoke: which key?")
	}
	e, err := d.devkeys.Revoke(req.DevKey.ID)
	if err != nil {
		return err
	}
	d.log.Info("device key revoked", "event", "devkey_revoked", "id", e.ID, "user", e.User, "name", e.Name)
	return w.Send(ctl.DevKeyReply{Entry: e})
}
