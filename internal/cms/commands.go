package cms

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// CreateUserWithRole is the command line's: a person on a role by name.
func (a *App) CreateUserWithRole(ctx context.Context, name, email, password, roleName string) (int64, error) {
	r, err := a.roleByName(ctx, roleName)
	if err != nil {
		return 0, fmt.Errorf("no role %q", roleName)
	}
	id, err := a.addUser(ctx, nil, name, email, password, r.ID)
	if e, ok := errors.AsType[*apierr.Error](err); ok && e.Errors != nil {
		return 0, fmt.Errorf("%v", e.Errors)
	}
	return id, err
}

// CreateServiceTokenForRole is the command line's: a token on a role by name.
func (a *App) CreateServiceTokenForRole(ctx context.Context, name, roleName string) (string, error) {
	r, err := a.roleByName(ctx, roleName)
	if err != nil {
		return "", fmt.Errorf("no role %q", roleName)
	}
	_, plain, err := a.issueServiceToken(ctx, nil, name, "Issued from the command line", r.ID)
	return plain, err
}

// DisableTwoFactor turns a user's second factor off: the command line's,
// for an account moved from the Rails core.
func (a *App) DisableTwoFactor(ctx context.Context, email string) error {
	res, err := a.store.ExecContext(ctx, `UPDATE users SET totp_enabled = 0, totp_secret = NULL, totp_enabled_at = NULL, recovery_code_digests = '[]', updated_at = ? WHERE lower(email) = lower(?)`, store.Now().DB(), email)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no user %s", email)
	}
	a.audit(ctx, nil, "user.two_factor_disabled", "User", 0, email, nil)
	return nil
}

// SetPassword is the command line's way back in for someone locked out.
func (a *App) SetPassword(ctx context.Context, email, password string) error {
	if problem := passwordProblem(password); problem != "" {
		return fmt.Errorf("the password %s", problem)
	}
	hash, err := secret.HashPassword(password)
	if err != nil {
		return err
	}
	var id int64
	err = a.store.Tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE lower(email) = lower(?)`, email).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no user %s", email)
		} else if err != nil {
			return fmt.Errorf("finding user %s: %w", email, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET password_digest = ?, updated_at = ? WHERE id = ?`, hash, store.Now().DB(), id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM admin_sessions WHERE user_id = ?`, id)
		return err
	})
	if err != nil {
		return err
	}
	a.audit(ctx, nil, "user.password_reset", "User", id, email, map[string]any{"via": "command line"})
	return nil
}
